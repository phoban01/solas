// Command solas-bench runs simulated members against one store and
// reports what the store sustains, spec 3.1 and 4.2 and issue #190.
//
//	solas-bench --storage-url <url> --members 4 --claims 8 --duration 10s
//
// Each simulated member has its own REST store of Devices, as each member
// cluster has its own API server, and so its own watch cache and poller.
// Each claim of a member binds a free device, releases it, and goes on,
// until the duration ends. The tool prints one row: binds per second,
// the conflict rate, the p50 and p99 latency of a bind, the watch lag,
// and the store reads per member per second.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"

	"github.com/phoban01/solas/pkg/apis/solas"
	"github.com/phoban01/solas/pkg/apiserver"
	"github.com/phoban01/solas/pkg/registry/solas/device"
	memberregistry "github.com/phoban01/solas/pkg/registry/solas/member"
	storageurl "github.com/phoban01/solas/pkg/storage"
	"github.com/phoban01/solas/pkg/storage/dynamo"
)

// options are the flags of the tool.
type options struct {
	storageURL string
	prefix     string
	members    int
	claims     int
	devices    int
	duration   time.Duration
	header     bool
	// memberLease, when set, gives each simulated member a Member object.
	memberLease time.Duration
	// maxUnits stops a run whose DynamoDB request units pass it.
	maxUnits int64
}

func main() {
	var o options
	fs := flag.NewFlagSet("solas-bench", flag.ExitOnError)
	fs.StringVar(&o.storageURL, "storage-url", "", "the store, as for solas --storage-url (required)")
	fs.StringVar(&o.prefix, "storage-prefix", "/registry/solas-bench", "key prefix in the store; the tool deletes every Device under it")
	fs.IntVar(&o.members, "members", 4, "simulated member clusters")
	fs.IntVar(&o.claims, "claims", 8, "claims per member, each binding and releasing in a loop")
	fs.IntVar(&o.devices, "devices", 0, "devices in the pool; 0 means members * claims")
	fs.DurationVar(&o.duration, "duration", 10*time.Second, "how long the claims run")
	fs.BoolVar(&o.header, "header", true, "print the header row")
	fs.Int64Var(&o.maxUnits, "max-units", 10_000_000, "stop the run when its DynamoDB request units (calls counted by the tool) pass this; a cost guard on a real table")
	fs.DurationVar(&o.memberLease, "member-lease", 0, "when set, each simulated member joins with a Member of this lease and renews it, so it can share a store with real clusters; 0 means no Member")
	_ = fs.Parse(os.Args[1:])
	if o.storageURL == "" {
		fmt.Fprintln(os.Stderr, "solas-bench: --storage-url is required")
		os.Exit(2)
	}
	if o.devices == 0 {
		o.devices = o.members * o.claims
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, o); err != nil {
		fmt.Fprintln(os.Stderr, "solas-bench:", err)
		os.Exit(1)
	}
}

// counting counts the calls to DynamoDB, so the tool can report reads per
// member. GetItem and Query are reads; TransactWriteItems is a write.
type counting struct {
	dynamo.API
	reads, writes atomic.Int64
}

func (c *counting) GetItem(ctx context.Context, in *dynamodb.GetItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	c.reads.Add(1)
	return c.API.GetItem(ctx, in, opts...)
}

func (c *counting) Query(ctx context.Context, in *dynamodb.QueryInput, opts ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	c.reads.Add(1)
	return c.API.Query(ctx, in, opts...)
}

func (c *counting) TransactWriteItems(ctx context.Context, in *dynamodb.TransactWriteItemsInput, opts ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error) {
	c.writes.Add(1)
	return c.API.TransactWriteItems(ctx, in, opts...)
}

// getter builds the REST options of the store. counter is nil on etcd.
func getter(ctx context.Context, o options) (apiserver.RESTOptionsGetter, *counting, error) {
	g := apiserver.RESTOptionsGetter{
		Codec:           apiserver.StorageCodec(),
		EncodeVersioner: apiserver.StorageVersioner(),
		Prefix:          o.prefix,
	}
	sc, err := storageurl.Parse(o.storageURL)
	if err != nil {
		return g, nil, fmt.Errorf("--storage-url: %w", err)
	}
	if sc.Kind == storageurl.Etcd {
		g.EtcdServers = sc.Endpoints
		return g, nil, nil
	}
	client, err := storageurl.DynamoClient(ctx, sc)
	if err != nil {
		return g, nil, err
	}
	if sc.CreateTable {
		if err := dynamo.EnsureTable(ctx, client, sc.Table); err != nil {
			return g, nil, err
		}
	}
	c := &counting{API: client}
	g.Dynamo = dynamo.Config{Client: c, Table: sc.Table, EventRetention: dynamo.DefaultEventRetention, PollInterval: dynamo.DefaultPollInterval}
	return g, c, nil
}

func reqCtx(ctx context.Context) context.Context {
	return genericapirequest.WithNamespace(genericapirequest.WithRequestInfo(ctx, &genericapirequest.RequestInfo{}), metav1.NamespaceNone)
}

// member is one simulated member cluster, with its own REST store.
type member struct {
	name    string
	uid     types.UID
	rest    *device.REST
	status  *device.StatusREST
	destroy func()
	// members and memberStatus are set when the member has a Member.
	members      *memberregistry.REST
	memberStatus *memberregistry.StatusREST
	lease        time.Duration
	memberRV     string
}

func newMember(g apiserver.RESTOptionsGetter, i int, lease time.Duration) (*member, error) {
	r, s, err := device.NewREST(apiserver.Scheme, g)
	if err != nil {
		return nil, err
	}
	m := &member{
		name: fmt.Sprintf("bench-m%02d", i), uid: types.UID(fmt.Sprintf("bench-member-uid-%02d", i)),
		rest: r, status: s, destroy: r.Store.DestroyFunc, lease: lease,
	}
	if lease > 0 {
		mr, ms, err := memberregistry.NewREST(apiserver.Scheme, g)
		if err != nil {
			r.Store.DestroyFunc()
			return nil, err
		}
		m.members, m.memberStatus = mr, ms
		m.destroy = func() {
			r.Store.DestroyFunc()
			mr.Store.DestroyFunc()
		}
	}
	return m, nil
}

// join creates the Member of a simulated member, as a member cluster
// does before it binds, spec 7.2, so the sweepers of real clusters on
// the same store treat its devices as held. It takes the real UID.
func (m *member) join(ctx context.Context) error {
	if m.lease == 0 {
		return nil
	}
	now := metav1.NewMicroTime(time.Now())
	obj, err := m.members.Create(reqCtx(ctx), &solas.Member{
		ObjectMeta: metav1.ObjectMeta{Name: m.name},
		Spec:       solas.MemberSpec{LeaseDurationSeconds: int32(m.lease / time.Second)},
		Status:     solas.MemberStatus{Phase: solas.MemberActive, RenewTime: &now},
	}, rest.ValidateAllObjectFunc, &metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("join %s: %w", m.name, err)
	}
	mem := obj.(*solas.Member)
	m.uid, m.memberRV = mem.UID, mem.ResourceVersion
	return nil
}

// renew renews the Member every lease / 3 until stop closes, spec 7.3.
func (m *member) renew(ctx context.Context, stop <-chan struct{}, st *stats) {
	if m.lease == 0 {
		return
	}
	t := time.NewTicker(m.lease / 3)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		}
		obj, err := m.members.Get(reqCtx(ctx), m.name, &metav1.GetOptions{})
		if err != nil {
			st.fail(ctx, fmt.Errorf("renew %s: %w", m.name, err))
			continue
		}
		mem := obj.(*solas.Member).DeepCopy()
		now := metav1.NewMicroTime(time.Now())
		mem.Status.RenewTime = &now
		if _, _, err := m.memberStatus.Update(reqCtx(ctx), m.name, rest.DefaultUpdatedObjectInfo(mem),
			rest.ValidateAllObjectFunc, rest.ValidateAllObjectUpdateFunc, false, &metav1.UpdateOptions{}); err != nil {
			st.fail(ctx, fmt.Errorf("renew %s: %w", m.name, err))
		}
	}
}

// leave deletes the Member at the end of the run.
func (m *member) leave(ctx context.Context) {
	if m.lease == 0 {
		return
	}
	if _, _, err := m.members.Delete(reqCtx(ctx), m.name, rest.ValidateAllObjectFunc, &metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		fmt.Fprintf(os.Stderr, "solas-bench: leave %s: %v\n", m.name, err)
	}
}

// list returns the devices from a consistent list, as the controller
// reads them before a bind, spec 6.3.
func (m *member) list(ctx context.Context) ([]solas.Device, string, error) {
	obj, err := m.rest.List(reqCtx(ctx), &metainternalversion.ListOptions{})
	if err != nil {
		return nil, "", err
	}
	l := obj.(*solas.DeviceList)
	return l.Items, l.ResourceVersion, nil
}

// writeRef sets or clears the claimRef of a device with a status update
// on the resource version of d, and returns the new version.
func (m *member) writeRef(ctx context.Context, d *solas.Device, ref *solas.ClaimRef) (string, error) {
	obj := d.DeepCopy()
	obj.Status.ClaimRef = ref
	out, _, err := m.status.Update(reqCtx(ctx), d.Name, rest.DefaultUpdatedObjectInfo(obj),
		rest.ValidateAllObjectFunc, rest.ValidateAllObjectUpdateFunc, false, &metav1.UpdateOptions{})
	if err != nil {
		return "", err
	}
	return out.(*solas.Device).ResourceVersion, nil
}

// stats collects what the run measures.
type stats struct {
	binds, releases, conflicts, errors atomic.Int64
	mu                                 sync.Mutex
	bindLatency                        []time.Duration
	listLatency                        []time.Duration
	watchLag                           []time.Duration
	// sync is the longest time a member's cache took to serve the whole
	// pool before the run, as an informer sync would.
	sync time.Duration
	// writes maps device/version to the time of its write, and arrivals
	// maps it to the first time a watch saw it, for whichever comes first.
	writes, arrivals sync.Map
}

func (s *stats) wrote(key string, at time.Time) {
	if seen, ok := s.arrivals.LoadAndDelete(key); ok {
		s.lag(seen.(time.Time).Sub(at))
		return
	}
	s.writes.Store(key, at)
}

func (s *stats) saw(key string, at time.Time) {
	if wrote, ok := s.writes.Load(key); ok {
		s.lag(at.Sub(wrote.(time.Time)))
		return
	}
	s.arrivals.LoadOrStore(key, at)
}

func (s *stats) lag(d time.Duration) {
	s.mu.Lock()
	s.watchLag = append(s.watchLag, d)
	s.mu.Unlock()
}

func (s *stats) bound(d time.Duration) {
	s.binds.Add(1)
	s.mu.Lock()
	s.bindLatency = append(s.bindLatency, d)
	s.mu.Unlock()
}

func (s *stats) listed(d time.Duration) {
	s.mu.Lock()
	s.listLatency = append(s.listLatency, d)
	s.mu.Unlock()
}

// percentile returns the p-th percentile of ds, or 0 when ds is empty.
func percentile(ds []time.Duration, p float64) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	// Nearest rank: the smallest value that at least p of the values are
	// at or below.
	i := int(math.Ceil(p*float64(len(ds)))) - 1
	return ds[max(0, min(i, len(ds)-1))]
}

func run(ctx context.Context, o options) error {
	g, counter, err := getter(ctx, o)
	if err != nil {
		return err
	}
	members := make([]*member, 0, o.members)
	defer func() {
		for _, m := range members {
			m.destroy()
		}
	}()
	// The first member prepares the pool. The others start once the pool
	// exists, so their first list measures a sync of the whole pool.
	admin, err := newMember(g, 0, o.memberLease)
	if err != nil {
		return err
	}
	members = append(members, admin)
	if err := reset(ctx, admin, o.devices); err != nil {
		return fmt.Errorf("prepare the device pool: %w", err)
	}
	// The run ends with the pool released and deleted, so a next run
	// starts clean and the store keeps no bench data.
	defer func() {
		if err := reset(context.Background(), admin, 0); err != nil {
			fmt.Fprintln(os.Stderr, "solas-bench: clean up:", err)
		}
	}()
	var st stats
	for i := 1; i < o.members; i++ {
		from := time.Now()
		m, err := newMember(g, i, o.memberLease)
		if err != nil {
			return err
		}
		members = append(members, m)
		if _, _, err := waitList(ctx, m, o.devices); err != nil {
			return err
		}
		st.sync = max(st.sync, time.Since(from))
	}
	if _, _, err := waitList(ctx, admin, o.devices); err != nil {
		return err
	}
	// With a lease, each member joins as a Member before it binds, spec
	// 7.2, and leaves at the end.
	for _, m := range members {
		if err := m.join(ctx); err != nil {
			return err
		}
	}
	defer func() {
		for _, m := range members {
			m.leave(context.Background())
		}
	}()

	// The workers stop between two requests when stop closes, so no
	// request is cut off in flight at the end of the run. The timer
	// starts once every worker runs, below.
	stop := make(chan struct{})
	var once sync.Once
	end := func() { once.Do(func() { close(stop) }) }
	go func() {
		select {
		case <-ctx.Done():
			end()
		case <-stop:
		}
	}()
	var wg sync.WaitGroup
	begin := make(chan struct{})
	for _, m := range members {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.renew(ctx, stop, &st)
		}()
		_, rv, err := m.list(ctx)
		if err != nil {
			return err
		}
		w, err := m.rest.Watch(reqCtx(ctx), &metainternalversion.ListOptions{ResourceVersion: rv})
		if err != nil {
			return fmt.Errorf("%s: watch: %w", m.name, err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer w.Stop()
			watchDevices(stop, w, &st)
		}()
		for c := range o.claims {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-begin
				claimLoop(ctx, stop, m, c, &st)
			}()
		}
	}
	var readsBefore, writesBefore int64
	if counter != nil {
		readsBefore, writesBefore = counter.reads.Load(), counter.writes.Load()
		// The cost guard: a run on a real table stops when its request
		// units pass --max-units, so a mistake stays small.
		go func() {
			t := time.NewTicker(time.Second)
			defer t.Stop()
			for {
				select {
				case <-stop:
					return
				case <-t.C:
				}
				used := counter.reads.Load() - readsBefore + counter.writes.Load() - writesBefore
				if overBudget(used, o.maxUnits) {
					fmt.Fprintf(os.Stderr, "solas-bench: stopped: %d request units passed --max-units %d\n", used, o.maxUnits)
					end()
					return
				}
			}
		}()
	}
	// Every watch runs; now the claims start together, and the clock.
	close(begin)
	timer := time.AfterFunc(o.duration, end)
	defer timer.Stop()
	start := time.Now()
	wg.Wait()
	elapsed := time.Since(start)
	if err := ctx.Err(); err != nil {
		return err
	}

	reads := "-"
	if counter != nil {
		perMember := float64(counter.reads.Load()-readsBefore) / float64(o.members) / elapsed.Seconds()
		reads = fmt.Sprintf("%.1f", perMember)
	}
	binds, conflicts := st.binds.Load(), st.conflicts.Load()
	conflictRate := 0.0
	if binds+conflicts > 0 {
		conflictRate = 100 * float64(conflicts) / float64(binds+conflicts)
	}
	store := "dynamodb"
	if counter == nil {
		store = "etcd"
	}
	ms := func(d time.Duration) string { return fmt.Sprintf("%.1f", float64(d.Microseconds())/1000) }
	cols := []string{"store", "members", "claims", "devices", "binds/s", "conflict%", "bind p50 ms", "bind p99 ms",
		"list p50 ms", "list p99 ms", "sync ms", "lag p50 ms", "lag p99 ms", "reads/member/s", "errors"}
	row := []string{store, fmt.Sprint(o.members), fmt.Sprint(o.claims), fmt.Sprint(o.devices),
		fmt.Sprintf("%.1f", float64(binds)/elapsed.Seconds()), fmt.Sprintf("%.1f", conflictRate),
		ms(percentile(st.bindLatency, 0.5)), ms(percentile(st.bindLatency, 0.99)),
		ms(percentile(st.listLatency, 0.5)), ms(percentile(st.listLatency, 0.99)), ms(st.sync),
		ms(percentile(st.watchLag, 0.5)), ms(percentile(st.watchLag, 0.99)),
		reads, fmt.Sprint(st.errors.Load())}
	if o.header {
		fmt.Println(strings.Join(cols, "\t"))
	}
	fmt.Println(strings.Join(row, "\t"))
	return nil
}

// waitList lists the devices until the cache of m serves want devices,
// or 30 seconds pass.
func waitList(ctx context.Context, m *member, want int) ([]solas.Device, string, error) {
	deadline := time.Now().Add(30 * time.Second)
	for {
		devices, rv, err := m.list(ctx)
		if err == nil && len(devices) == want {
			return devices, rv, nil
		}
		if time.Now().After(deadline) {
			if err == nil {
				err = fmt.Errorf("list has %d devices, want %d", len(devices), want)
			}
			return nil, "", fmt.Errorf("%s: %w", m.name, err)
		}
		select {
		case <-ctx.Done():
			return nil, "", ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// reset deletes every device under the prefix, releasing held ones first,
// and then creates want new devices.
func reset(ctx context.Context, m *member, want int) error {
	devices, _, err := waitListAny(ctx, m)
	if err != nil {
		return err
	}
	// Only the bench's own devices: a shared store has others.
	var own []*solas.Device
	for i := range devices {
		if devices[i].Spec.Attributes["bench"] == "true" {
			own = append(own, &devices[i])
		}
	}
	// Sixteen at a time: on a real table each call is a round trip, and a
	// pool of 10,000 devices would take minutes one by one.
	err = each(ctx, len(own), func(i int) error {
		d := own[i]
		if d.Status.ClaimRef != nil {
			if _, err := m.writeRef(ctx, d, nil); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("release %s: %w", d.Name, err)
			}
		}
		if _, _, err := m.rest.Delete(reqCtx(ctx), d.Name, rest.ValidateAllObjectFunc, &metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete %s: %w", d.Name, err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return each(ctx, want, func(i int) error {
		d := &solas.Device{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("bench-%04d", i), Labels: map[string]string{"bench": "true"}}}
		d.Spec.Attributes = map[string]string{"bench": "true"}
		if _, err := m.rest.Create(reqCtx(ctx), d, rest.ValidateAllObjectFunc, &metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("create %s: %w", d.Name, err)
		}
		return nil
	})
}

// each runs f for 0 to n-1 with sixteen workers and returns the first
// error.
func each(ctx context.Context, n int, f func(i int) error) error {
	const workers = 16
	var wg sync.WaitGroup
	idx := make(chan int)
	var firstErr error
	var mu sync.Mutex
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range idx {
				if err := f(i); err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
				}
			}
		}()
	}
	for i := 0; i < n; i++ {
		select {
		case <-ctx.Done():
			i = n
		case idx <- i:
		}
	}
	close(idx)
	wg.Wait()
	if firstErr == nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return firstErr
}

// waitListAny lists the devices until the cache answers.
func waitListAny(ctx context.Context, m *member) ([]solas.Device, string, error) {
	deadline := time.Now().Add(30 * time.Second)
	for {
		devices, rv, err := m.list(ctx)
		if err == nil || time.Now().After(deadline) {
			return devices, rv, err
		}
		select {
		case <-ctx.Done():
			return nil, "", ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// watchDevices records when each change of a device reaches this member.
func watchDevices(stop <-chan struct{}, w watch.Interface, st *stats) {
	for {
		select {
		case <-stop:
			return
		case e, ok := <-w.ResultChan():
			if !ok {
				return
			}
			if e.Type != watch.Modified {
				continue
			}
			if a, err := meta.Accessor(e.Object); err == nil {
				st.saw(a.GetName()+"/"+a.GetResourceVersion(), time.Now())
			}
		}
	}
}

// claimLoop is one claim of a member: list, bind a free device, release
// it, and go on until stop closes.
func claimLoop(ctx context.Context, stop <-chan struct{}, m *member, claim int, st *stats) {
	ref := &solas.ClaimRef{Member: m.name, MemberUID: m.uid, Namespace: "bench",
		Name: fmt.Sprintf("c%02d", claim), UID: types.UID(fmt.Sprintf("%s-c%02d", m.uid, claim))}
	rng := rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64()))
	for {
		select {
		case <-stop:
			return
		default:
		}
		from := time.Now()
		devices, _, err := m.list(ctx)
		if err != nil {
			st.fail(ctx, err)
			continue
		}
		st.listed(time.Since(from))
		var free []*solas.Device
		for i := range devices {
			if devices[i].Status.ClaimRef == nil && devices[i].Spec.Attributes["bench"] == "true" {
				free = append(free, &devices[i])
			}
		}
		if len(free) == 0 {
			select {
			case <-stop:
				return
			case <-time.After(10 * time.Millisecond):
			}
			continue
		}
		d := free[rng.IntN(len(free))]
		now := metav1.Now()
		ref.BoundAt = &now
		start := time.Now()
		rv, err := m.writeRef(ctx, d, ref)
		switch {
		case err == nil:
			st.bound(time.Since(start))
			st.wrote(d.Name+"/"+rv, time.Now())
		case apierrors.IsConflict(err):
			st.conflicts.Add(1)
			continue
		default:
			st.fail(ctx, err)
			continue
		}
		held := d.DeepCopy()
		held.ResourceVersion = rv
		held.Status.ClaimRef = ref.DeepCopy()
		rv, err = m.writeRef(ctx, held, nil)
		switch {
		case err == nil:
			st.releases.Add(1)
			st.wrote(d.Name+"/"+rv, time.Now())
		default:
			// A release never conflicts: only the holder writes a held
			// device. The device stays held until the cleanup.
			st.fail(ctx, err)
		}
	}
}

// fail counts an error of the run. The end of the run is not an error.
func (s *stats) fail(ctx context.Context, err error) {
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return
	}
	s.errors.Add(1)
	if n := s.errors.Load(); n <= 5 {
		fmt.Fprintln(os.Stderr, "solas-bench:", err)
	}
}

// overBudget reports whether used request units passed the limit. A
// limit of 0 or less means no limit.
func overBudget(used, limit int64) bool { return limit > 0 && used > limit }
