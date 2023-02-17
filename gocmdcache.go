// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package gocmdcache

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// This package provides a mechanism for performing "go" commands
// (such as "go list" or "go build") and then caching the results, so
// that repeated identical queries will return more quickly.

type Cache struct {
	listcachemu    sync.Mutex
	listcache      map[string]*Pkg
	pkgsizecachemu sync.Mutex
	pkgsizecache   map[string]PkgInfo
	root           string
	repohash       string
	goroothash     string
	vlevel         int
}

func (c *Cache) verb(vlevel int, s string, a ...interface{}) {
	if c.vlevel >= vlevel {
		fmt.Printf(s, a...)
		fmt.Printf("\n")
	}
}

const glopath = "=glo="

func Make(repohash, goroothash, rootcachedir string, verblevel int) (*Cache, error) {
	if err := os.Mkdir(rootcachedir, 0777); err != nil {
		if !os.IsExist(err) {
			return nil, fmt.Errorf("unable to create cache %s: %v",
				rootcachedir, err)
		}
	}
	rv := &Cache{
		listcache:    make(map[string]*Pkg),
		pkgsizecache: make(map[string]PkgInfo),
		root:         rootcachedir,
		repohash:     repohash,
		goroothash:   goroothash,
		vlevel:       verblevel,
	}
	if err := rv.checkValid(); err != nil {
		return nil, err
	}
	return rv, nil
}

// Pkg holds results from "go list -json". There are many more
// fields we could ask for, but at the moment we just need a few.
type Pkg struct {
	Standard   bool
	ImportPath string
	Root       string
	Imports    []string
}

// PkgInfo holds approximate estimates of package size, obtained
// from "go build" (for non-main packages).
type PkgInfo struct {
	Size     int
	NumFuncs int
}

// Cache of package sizes from gobuild with associated mutex
func (c *Cache) writeToken() error {
	p := filepath.Join(c.root, glopath)
	outf, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("opening %s: %v", p, err)
	}
	if _, err := fmt.Fprintf(outf, "%s %s\n", c.repohash, c.goroothash); err != nil {
		return fmt.Errorf("writing %s: %v", p, err)
	}
	if err := outf.Close(); err != nil {
		return err
	}
	return nil
}

func (c *Cache) checkValid() error {
	p := filepath.Join(c.root, glopath)
	contents, err := os.ReadFile(p)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
	}
	val := strings.TrimSpace(string(contents))
	want := c.repohash + " " + c.goroothash
	if val != want {
		c.verb(2, "cache mismatch:\ngot %q\nwant %q", val, want)
		if err := os.RemoveAll(c.root); err != nil {
			return err
		}
		if err := os.Mkdir(c.root, 0777); err != nil {
			return err
		}
		if err := c.writeToken(); err != nil {
			return err
		}
	}
	return nil
}

func (c *Cache) cachePath(dir string, tag string) string {
	dtag := strings.ReplaceAll(dir, "/", "%")
	return filepath.Join(c.root, dtag+"."+tag)
}

func (c *Cache) tryCache(dir string, tag string) ([]byte, bool, error) {
	if err := c.checkValid(); err != nil {
		return nil, false, fmt.Errorf("problems reading cache %s: %v",
			c.root, err)
	}
	contents, err := os.ReadFile(c.cachePath(dir, tag))
	if err != nil {
		if os.IsNotExist(err) {
			c.verb(3, "%s cache miss on %s", tag, dir)
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("problems reading cache %s: %v",
			c.root, err)
	}
	c.verb(3, "%s cache hit on %s", tag, dir)
	return contents, true, nil
}

func (c *Cache) WriteCache(dir, tag string, content []byte) error {
	c.verb(2, "%s cache write for %s", tag, dir)
	if err := os.WriteFile(c.cachePath(dir, tag), content, 0777); err != nil {
		return err
	}
	return nil
}

func (c *Cache) GoList(dir string) (*Pkg, error) {
	// Try mem cache first
	c.listcachemu.Lock()
	cpk, ok := c.listcache[dir]
	c.listcachemu.Unlock()
	if ok {
		return cpk, nil
	}
	// Try disk cache next
	var pkg Pkg
	out, valid, err := c.tryCache(dir, "list")
	if err != nil {
		return nil, err
	} else if !valid {
		// cache miss, run "go list"
		pk, out, err := goListUncached(dir, "")
		if err != nil {
			return nil, err
		}
		c.listcachemu.Lock()
		c.listcache[dir] = pk
		c.listcachemu.Unlock()
		// write back to cache
		if err := c.WriteCache(dir, "list", out); err != nil {
			return nil, fmt.Errorf("writing cache: %v", err)
		}
		return pk, nil
	}
	// unpack
	if err := json.Unmarshal(out, &pkg); err != nil {
		return nil, fmt.Errorf("go list -json %s: unmarshal: %v", dir, err)
	}
	c.listcachemu.Lock()
	c.listcache[dir] = &pkg
	c.listcachemu.Unlock()
	return &pkg, nil
}

func goListUncached(tgt, dir string) (*Pkg, []byte, error) {
	// run "go list"
	cmd := exec.Command("go", "list", "-json", tgt)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.Output()
	if err != nil {
		return nil, nil, fmt.Errorf("go list -json %s: %v", dir, err)
	}
	// unpack
	var pkg Pkg
	if err := json.Unmarshal(out, &pkg); err != nil {
		return nil, nil, fmt.Errorf("go list -json %s: unmarshal: %v", dir, err)
	}
	return &pkg, out, nil
}

// computePkgInfo given a compiled package file 'apath' returns
// a string of the form "N M" where N is the compiled package file
// size, and M is the estimated number of functions it contains.
func computePkgInfo(apath string) (string, error) {
	// compute file size
	fi, ferr := os.Stat(apath)
	if ferr != nil {
		return "", fmt.Errorf("stat on %s: %v", apath, ferr)
	}
	// compute func count estimate
	cmd := exec.Command("go", "tool", "nm", apath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("go tool nm %s: %v", apath, err)
	}
	lines := strings.Split(string(out), "\n")
	totf := 0
	for _, line := range lines {
		m := strings.Fields(line)
		if len(m) == 3 && m[1] == "T" {
			totf++
			continue
		}
		if len(m) == 4 && m[2] == "T" {
			totf++
			continue
		}
	}
	return fmt.Sprintf("%d %d\n", fi.Size(), totf), nil
}

func (c *Cache) PkgSize(dir string) (PkgInfo, error) {
	// special case for unsafe
	if dir == "unsafe" {
		return PkgInfo{Size: 1, NumFuncs: 0}, nil
	}
	// Try mem cache first
	c.pkgsizecachemu.Lock()
	cachedv, ok := c.pkgsizecache[dir]
	c.pkgsizecachemu.Unlock()
	if ok {
		return cachedv, nil
	}
	// Try disk cache next
	out, valid, err := c.tryCache(dir, "build")
	if err != nil {
		return PkgInfo{}, err
	} else if !valid {
		// cache miss, run "go build"
		outfile := c.cachePath(dir, "archive")
		os.RemoveAll(outfile)
		c.verb(2, "build cmd is 'go build -o %s %s", outfile, dir)
		cmd := exec.Command("go", "build", "-o", outfile, dir)
		out, err = cmd.CombinedOutput()
		if err != nil {
			c.verb(0, "failed build output: %s", string(out))
			return PkgInfo{}, fmt.Errorf("go build %s: %v", dir, err)
		}
		payload, perr := computePkgInfo(outfile)
		if perr != nil {

			return PkgInfo{}, perr
		}
		out = []byte(payload)
		// write back size to cache
		if err := c.WriteCache(dir, "build", out); err != nil {
			return PkgInfo{}, fmt.Errorf("writing cache: %v", err)
		}
		os.Remove(outfile)
	}
	// unpack
	var sz int
	var nf int
	if n, err := fmt.Sscanf(string(out), "%d %d", &sz, &nf); err != nil || n != 2 {
		return PkgInfo{}, fmt.Errorf("interpreting pksize %s: %v", string(out), err)
	}
	pi := PkgInfo{Size: sz, NumFuncs: nf}
	c.pkgsizecachemu.Lock()
	c.pkgsizecache[dir] = pi
	c.pkgsizecachemu.Unlock()

	return pi, nil
}
