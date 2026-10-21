package app

import (
	"os"
	"sync"
	"time"
)

var cached struct {
	sync.Mutex
	path string
	mod  time.Time
	size int64
	c    Config
}

// LoadCached is Load, but it reads the file again only when the file
// changed. Use it for status screens that ask every few seconds.
func LoadCached() (*Config, error) {
	p := configPath()
	info, err := os.Stat(p)
	if err != nil {
		return Load()
	}
	cached.Lock()
	defer cached.Unlock()
	if cached.path == p && info.ModTime().Equal(cached.mod) && info.Size() == cached.size {
		c := cached.c
		return &c, nil
	}
	c, err := Load()
	if err != nil {
		return nil, err
	}
	cached.path, cached.mod, cached.size, cached.c = p, info.ModTime(), info.Size(), *c
	cp := *c
	return &cp, nil
}
