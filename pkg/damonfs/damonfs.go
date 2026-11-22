package damonfs

import (
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
)

const DefaultRoot = "/sys/kernel/mm/damon/admin"

type (
	FS struct{ b Backend }
)

var (
	ErrNotSupported       = errors.New("damonfs: DAMON sysfs interface not available")
	ErrRunning            = errors.New("damonfs: kdamond is running")
	ErrUnsupportedFeature = errors.New("damonfs: feature not supported by this kernel")
)

func Open(root string) (*FS, error) {
	if root == "" {
		root = DefaultRoot
	}
	b := OSBackend(root)
	if !b.Exists("kdamonds/nr_kdamonds") {
		return nil, fmt.Errorf("%w at %s (needs CONFIG_DAMON_SYSFS and /sys mounted read-write)", ErrNotSupported, root)
	}
	return &FS{b: b}, nil
}

func New(b Backend) *FS { return &FS{b: b} }

func (f *FS) NrKdamonds() (int, error) {
	n, err := f.readUint("kdamonds/nr_kdamonds")
	return int(n), err
}

func (f *FS) SetNrKdamonds(n int) error {
	if n < 0 {
		return fmt.Errorf("damonfs: negative kdamond count %d", n)
	}
	return f.writeUint("kdamonds/nr_kdamonds", uint64(n))
}

func (f *FS) Reserve(n int) error {
	cur, err := f.NrKdamonds()
	if err != nil {
		return err
	}
	if cur >= n {
		return nil
	}
	return f.SetNrKdamonds(n)
}

func (f *FS) Kdamond(i int) *Kdamond {
	return &Kdamond{fs: f, idx: i, dir: path.Join("kdamonds", strconv.Itoa(i))}
}

func (f *FS) read(name string) (string, error) {
	b, err := f.b.ReadFile(name)
	if err != nil {
		return "", fmt.Errorf("damonfs: read %s: %w", name, err)
	}
	return strings.TrimSpace(string(b)), nil
}

func (f *FS) readUint(name string) (uint64, error) {
	s, err := f.read(name)
	if err != nil {
		return 0, err
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("damonfs: parse %s: %w", name, err)
	}
	return v, nil
}

func (f *FS) write(name, val string) error {
	if err := f.b.WriteFile(name, []byte(val)); err != nil {
		return fmt.Errorf("damonfs: write %q to %s: %w", val, name, err)
	}
	return nil
}

func (f *FS) writeUint(name string, v uint64) error {
	return f.write(name, strconv.FormatUint(v, 10))
}
