package apiserver

import "testing"

func TestStorageCodecFor(t *testing.T) {
	if _, _, err := StorageCodecFor("v1alpha1"); err != nil {
		t.Errorf("v1alpha1: %v", err)
	}
	for _, v := range []string{"v9", "", "v1"} {
		if _, _, err := StorageCodecFor(v); err == nil {
			t.Errorf("StorageCodecFor(%q) passed, want an error", v)
		}
	}
}
