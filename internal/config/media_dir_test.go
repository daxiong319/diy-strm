package config

import (
	"os"
	"reflect"
	"testing"
)

func TestMediaDirDisabledByDefault(t *testing.T) {
	t.Setenv("LITEPAN_DATA_DIR", "")
	t.Setenv("LITEPAN_MEDIA_DIR", "")
	t.Setenv("LITEPAN_MEDIA_DIRS", "")
	_ = os.Unsetenv("LITEPAN_MEDIA_DIR")
	_ = os.Unsetenv("LITEPAN_MEDIA_DIRS")
	cfg := Load()
	if cfg.MediaDir != "" {
		t.Fatalf("MediaDir=%q, want empty (feature disabled)", cfg.MediaDir)
	}
	if roots := cfg.MediaRoots(); len(roots) != 0 {
		t.Fatalf("MediaRoots()=%v, want empty", roots)
	}
}

func TestLoadMediaDirExplicitOverride(t *testing.T) {
	t.Setenv("LITEPAN_MEDIA_DIR", "/media/library")
	t.Setenv("LITEPAN_MEDIA_DIRS", "")
	_ = os.Unsetenv("LITEPAN_MEDIA_DIRS")
	cfg := Load()
	if cfg.MediaDir != "/media/library" {
		t.Fatalf("MediaDir=%q, want /media/library", cfg.MediaDir)
	}
	if got := cfg.MediaRoots(); !reflect.DeepEqual(got, []string{"/media/library"}) {
		t.Fatalf("MediaRoots()=%v", got)
	}
}

func TestLoadMediaDirsCommaSeparated(t *testing.T) {
	t.Setenv("LITEPAN_MEDIA_DIR", "/media/a")
	t.Setenv("LITEPAN_MEDIA_DIRS", "/media/b, /media/c ,,")
	cfg := Load()
	want := []string{"/media/a", "/media/b", "/media/c"}
	if got := cfg.MediaRoots(); !reflect.DeepEqual(got, want) {
		t.Fatalf("MediaRoots()=%v, want %v", got, want)
	}
}

func TestLoadMediaDirsOnly(t *testing.T) {
	t.Setenv("LITEPAN_MEDIA_DIR", "")
	t.Setenv("LITEPAN_MEDIA_DIRS", "/mnt/x,/mnt/y")
	_ = os.Unsetenv("LITEPAN_MEDIA_DIR")
	cfg := Load()
	want := []string{"/mnt/x", "/mnt/y"}
	if got := cfg.MediaRoots(); !reflect.DeepEqual(got, want) {
		t.Fatalf("MediaRoots()=%v, want %v", got, want)
	}
}
