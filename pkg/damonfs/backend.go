package damonfs

import (
	"os"
	"path/filepath"
	"sort"
)

type (
	Backend interface {
		ReadFile(name string) ([]byte, error)
		WriteFile(name string, data []byte) error
		ReadDir(name string) ([]string, error)
		Exists(name string) bool
	}

	osBackend struct{ root string }
)

func OSBackend(root string) Backend { return osBackend{root: root} }

func (b osBackend) path(name string) string {
	return filepath.Join(b.root, filepath.FromSlash(name))
}

func (b osBackend) ReadFile(name string) ([]byte, error) {
	return os.ReadFile(b.path(name))
}

func (b osBackend) WriteFile(name string, data []byte) error {
	f, err := os.OpenFile(b.path(name), os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	return cerr
}

func (b osBackend) ReadDir(name string) ([]string, error) {
	entries, err := os.ReadDir(b.path(name))
	if err != nil {
		return nil, err
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name()
	}
	sort.Strings(names)
	return names, nil
}

func (b osBackend) Exists(name string) bool {
	_, err := os.Stat(b.path(name))
	return err == nil
}
