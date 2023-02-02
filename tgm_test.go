// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package gocmdcache_test

import (
	"path/filepath"
	"testing"

	"github.com/thanm/gocmdcache"
)

func TestGoList(t *testing.T) {
	tdir := t.TempDir()
	cachedir := filepath.Join(tdir, "cachedir")
	c, err := gocmdcache.Make("xyz", "def", cachedir, 3)
	if err != nil {
		t.Fatalf("Make returns %v", err)
	}
	p, err := c.GoList("unsafe")
	if err != nil {
		t.Errorf("list of unsafe: %v", err)
	}
	if p.ImportPath != "unsafe" || p.Standard != true {
		t.Errorf("bad return on unsafe from golist: %+v", p)
	}
	p, err = c.GoList("io")
	if err != nil {
		t.Errorf("list of io: %v", err)
	}
	if p.ImportPath != "io" || p.Standard != true {
		t.Errorf("bad return on io from golist: %+v", p)
	}
	p, err = c.GoList("unsafe")
	if err != nil {
		t.Errorf("list of unsafe: %v", err)
	}
	if p.ImportPath != "unsafe" || p.Standard != true {
		t.Errorf("bad return on unsafe from golist: %+v", p)
	}
}

func TestPkgSize(t *testing.T) {
	tdir := t.TempDir()
	cachedir := filepath.Join(tdir, "cachedir")
	c, err := gocmdcache.Make("qrs", "abc", cachedir, 3)
	if err != nil {
		t.Fatalf("Make returns %v", err)
	}
	p, err := c.PkgSize("unsafe")
	if err != nil {
		t.Errorf("pkgsize of unsafe: %v", err)
	}
	if p.Size != 1 || p.NumFuncs != 0 {
		t.Errorf("bad return on unsafe from gopkgsize: %+v", p)
	}
	p, err = c.PkgSize("io")
	if err != nil {
		t.Errorf("pkgsize of io: %v", err)
	}

	if p.Size <= 0 || p.NumFuncs <= 0 {
		t.Errorf("bad return on io from gopkgsize: %+v", p)
	}
	p2, err2 := c.PkgSize("io")
	if err2 != nil {
		t.Errorf("second pkgsize of io: %v", err2)
	}
	if p != p2 {
		t.Errorf("bad return on second size of io: %+v", p2)
	}
}
