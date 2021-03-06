// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package dashboard

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestOSARCHAccessors(t *testing.T) {
	valid := func(s string) bool { return s != "" && !strings.Contains(s, "-") }
	for _, conf := range Builders {
		os := conf.GOOS()
		arch := conf.GOARCH()
		osArch := os + "-" + arch
		if !valid(os) || !valid(arch) || !(conf.Name == osArch || strings.HasPrefix(conf.Name, osArch+"-")) {
			t.Errorf("OS+ARCH(%q) = %q, %q; invalid", conf.Name, os, arch)
		}
	}
}

func TestDistTestsExecTimeout(t *testing.T) {
	tests := []struct {
		c    *BuildConfig
		want time.Duration
	}{
		{
			&BuildConfig{
				env:          []string{},
				testHostConf: &HostConfig{},
			},
			20 * time.Minute,
		},
		{
			&BuildConfig{
				env:          []string{"GO_TEST_TIMEOUT_SCALE=2"},
				testHostConf: &HostConfig{},
			},
			40 * time.Minute,
		},
		{
			&BuildConfig{
				env: []string{},
				testHostConf: &HostConfig{
					env: []string{"GO_TEST_TIMEOUT_SCALE=3"},
				},
			},
			60 * time.Minute,
		},
		// BuildConfig's env takes precedence:
		{
			&BuildConfig{
				env: []string{"GO_TEST_TIMEOUT_SCALE=2"},
				testHostConf: &HostConfig{
					env: []string{"GO_TEST_TIMEOUT_SCALE=3"},
				},
			},
			40 * time.Minute,
		},
	}
	for i, tt := range tests {
		got := tt.c.DistTestsExecTimeout(nil)
		if got != tt.want {
			t.Errorf("%d. got %v; want %v", i, got, tt.want)
		}
	}
}

// TestTrybots tests that a given repo & its branch yields the provided
// complete set of builders. See also: TestBuilders, which tests both trybots
// and post-submit builders, both at arbitrary branches.
func TestTrybots(t *testing.T) {
	tests := []struct {
		repo   string // "go", "net", etc
		branch string // of repo
		want   []string
	}{
		{
			repo:   "go",
			branch: "master",
			want: []string{
				"android-amd64-emu",
				"freebsd-amd64-12_0",
				"js-wasm",
				"linux-386",
				"linux-amd64",
				"linux-amd64-race",
				"misc-compile-other",
				"misc-compile-darwin",
				"misc-compile-linuxarm",
				"misc-compile-solaris",
				"misc-compile-freebsd",
				"misc-compile-mips",
				"misc-compile-nacl",
				"misc-compile-netbsd",
				"misc-compile-openbsd",
				"misc-compile-plan9",
				"misc-compile-ppc",
				"nacl-amd64p32",
				"openbsd-amd64-64",
				"windows-386-2008",
				"windows-amd64-2016",
			},
		},
		{
			repo:   "go",
			branch: "release-branch.go1.12",
			want: []string{
				"freebsd-amd64-10_3",
				"freebsd-amd64-12_0",
				"js-wasm",
				"linux-386",
				"linux-amd64",
				"linux-amd64-race",
				"misc-compile-darwin",
				"misc-compile-freebsd",
				"misc-compile-linuxarm",
				"misc-compile-mips",
				"misc-compile-nacl",
				"misc-compile-netbsd",
				"misc-compile-openbsd",
				"misc-compile-other",
				"misc-compile-plan9",
				"misc-compile-ppc",
				"misc-compile-solaris",
				"nacl-amd64p32",
				"openbsd-amd64-64",
				"windows-386-2008",
				"windows-amd64-2016",
			},
		},
		{
			repo:   "mobile",
			branch: "master",
			want: []string{
				"android-amd64-emu",
				"linux-amd64-androidemu",
			},
		},
		{
			repo:   "sys",
			branch: "master",
			want: []string{
				"android-amd64-emu",
				"freebsd-386-11_2",
				"freebsd-amd64-11_2",
				"freebsd-amd64-12_0",
				"linux-386",
				"linux-amd64",
				"linux-amd64-race",
				"netbsd-amd64-8_0",
				"openbsd-386-64",
				"openbsd-amd64-64",
				"windows-386-2008",
				"windows-amd64-2016",
			},
		},
		{
			repo:   "exp",
			branch: "master",
			want: []string{
				"linux-amd64",
				"linux-amd64-race",
				"windows-386-2008",
				"windows-amd64-2016",
			},
		},
	}
	for i, tt := range tests {
		if tt.branch == "" || tt.repo == "" {
			t.Errorf("incomplete test entry %d", i)
			return
		}
		t.Run(fmt.Sprintf("%s/%s", tt.repo, tt.branch), func(t *testing.T) {
			var got []string
			goBranch := tt.branch // hard-code the common case for now
			for _, bc := range TryBuildersForProject(tt.repo, tt.branch, goBranch) {
				got = append(got, bc.Name)
			}
			m := map[string]bool{}
			for _, b := range tt.want {
				m[b] = true
			}
			for _, b := range got {
				if _, ok := m[b]; !ok {
					t.Errorf("got unexpected %q", b)
				}
				delete(m, b)
			}
			for b := range m {
				t.Errorf("missing expected %q", b)
			}
		})
	}
}

// TestBuilderConfig whether a given builder and repo at different
// branches is either a post-submit builder, trybot, neither, or both.
func TestBuilderConfig(t *testing.T) {
	// builderConfigWant is bitmask of 4 different things to assert are wanted:
	// - being a post-submit builder
	// - NOT being a post-submit builder
	// - being a trybot builder
	// - NOT being a post-submit builder
	type want uint8
	const (
		isTrybot want = 1 << iota
		notTrybot
		isBuilder  // post-submit
		notBuilder // not post-submit

		none     = notTrybot + notBuilder
		both     = isTrybot + isBuilder
		onlyPost = notTrybot + isBuilder
	)

	type builderAndRepo struct {
		testName string
		builder  string
		repo     string
		branch   string
		goBranch string
	}
	// builder may end in "@go1.N" (as alias for "@release-branch.go1.N") or "@branch-name".
	// repo may end in "@1.N" (as alias for "@release-branch.go1.N")
	b := func(builder, repo string) builderAndRepo {
		br := builderAndRepo{
			testName: builder + "," + repo,
			builder:  builder,
			goBranch: "master",
			repo:     repo,
			branch:   "master",
		}
		if strings.Contains(builder, "@") {
			f := strings.SplitN(builder, "@", 2)
			br.builder = f[0]
			br.goBranch = f[1]
		}
		if strings.Contains(repo, "@") {
			f := strings.SplitN(repo, "@", 2)
			br.repo = f[0]
			br.branch = f[1]
		}
		expandBranch := func(s *string) {
			if strings.HasPrefix(*s, "go1.") {
				*s = "release-branch." + *s
			} else if strings.HasPrefix(*s, "1.") {
				*s = "release-branch.go" + *s
			}
		}
		expandBranch(&br.branch)
		expandBranch(&br.goBranch)
		if br.repo == "go" {
			br.branch = br.goBranch
		}
		return br
	}
	tests := []struct {
		br   builderAndRepo
		want want
	}{
		{b("linux-amd64", "go"), both},
		{b("linux-amd64", "net"), both},
		{b("linux-amd64", "sys"), both},
		{b("linux-amd64", "website"), both},

		// Don't test all subrepos on all the builders.
		{b("linux-amd64-ssacheck", "net"), none},
		{b("linux-amd64-ssacheck@go1.10", "net"), none},
		{b("linux-amd64-noopt@go1.11", "net"), none},
		{b("linux-386-387@go1.11", "net"), none},
		{b("linux-386-387@go1.11", "go"), onlyPost},
		{b("linux-386-387", "crypto"), onlyPost},
		{b("linux-arm-arm5spacemonkey@go1.11", "net"), none},
		{b("linux-arm-arm5spacemonkey@go1.12", "net"), none},

		// The mobile repo requires Go 1.13+.
		{b("android-amd64-emu", "go"), both},
		{b("android-amd64-emu", "mobile"), both},
		{b("android-amd64-emu", "mobile@1.10"), none},
		{b("android-amd64-emu", "mobile@1.11"), none},
		{b("android-amd64-emu@go1.10", "mobile"), none},
		{b("android-amd64-emu@go1.11", "mobile"), none},
		{b("android-amd64-emu@go1.12", "mobile"), none},
		{b("android-amd64-emu@go1.13", "mobile"), both},
		{b("android-amd64-emu", "mobile@1.13"), both},
		{b("android-amd64-emu", "crypto"), both},
		{b("android-amd64-emu", "net"), both},
		{b("android-amd64-emu", "sync"), both},
		{b("android-amd64-emu", "sys"), both},
		{b("android-amd64-emu", "text"), both},
		{b("android-amd64-emu", "time"), both},
		{b("android-amd64-emu", "tools"), both},

		{b("android-386-emu", "go"), onlyPost},
		{b("android-386-emu", "mobile"), onlyPost},
		{b("android-386-emu", "mobile@1.10"), none},
		{b("android-386-emu", "mobile@1.11"), none},
		{b("android-386-emu@go1.10", "mobile"), none},
		{b("android-386-emu@go1.11", "mobile"), none},
		{b("android-386-emu@go1.12", "mobile"), none},
		{b("android-386-emu@go1.13", "mobile"), onlyPost},
		{b("android-386-emu", "mobile@1.13"), onlyPost},

		{b("linux-amd64", "net"), both},
		{b("linux-amd64", "net@1.12"), both},
		{b("linux-amd64@go1.12", "net@1.12"), both},
		{b("linux-amd64", "net@1.11"), both},
		{b("linux-amd64", "net@1.11"), both},
		{b("linux-amd64", "net@1.10"), none},   // too old
		{b("linux-amd64@go1.10", "net"), none}, // too old
		{b("linux-amd64@go1.11", "net"), both},
		{b("linux-amd64@go1.11", "net@1.11"), both},
		{b("linux-amd64@go1.12", "net@1.12"), both},

		// go1.12.html: "Go 1.12 is the last release that is
		// supported on FreeBSD 10.x [... and 11.1]"
		{b("freebsd-386-10_3", "go"), none},
		{b("freebsd-386-10_3", "net"), none},
		{b("freebsd-amd64-10_3", "go"), none},
		{b("freebsd-amd64-10_3", "net"), none},
		{b("freebsd-amd64-11_1", "go"), none},
		{b("freebsd-amd64-11_1", "net"), none},
		{b("freebsd-amd64-10_3@go1.12", "go"), both},
		{b("freebsd-amd64-10_3@go1.12", "net@1.12"), both},
		{b("freebsd-amd64-10_3@go1.11", "go"), both},
		{b("freebsd-amd64-10_3@go1.11", "net@1.11"), both},
		{b("freebsd-amd64-11_1@go1.13", "go"), none},
		{b("freebsd-amd64-11_1@go1.13", "net@1.12"), none},
		{b("freebsd-amd64-11_1@go1.12", "go"), isBuilder},
		{b("freebsd-amd64-11_1@go1.12", "net@1.12"), isBuilder},
		{b("freebsd-amd64-11_1@go1.11", "go"), isBuilder},
		{b("freebsd-amd64-11_1@go1.11", "net@1.11"), isBuilder},

		// FreeBSD 12.0
		{b("freebsd-amd64-12_0", "go"), both},
		{b("freebsd-amd64-12_0", "net"), both},
		{b("freebsd-386-12_0", "go"), onlyPost},
		{b("freebsd-386-12_0", "net"), onlyPost},

		// NetBSD
		{b("netbsd-amd64-8_0", "go"), onlyPost},
		{b("netbsd-amd64-8_0", "net"), onlyPost},
		{b("netbsd-386-8_0", "go"), none},
		{b("netbsd-386-8_0", "net"), none},

		// AIX starts at Go 1.12
		{b("aix-ppc64", "go"), onlyPost},
		{b("aix-ppc64", "net"), onlyPost},
		{b("aix-ppc64", "mobile"), none},
		{b("aix-ppc64", "exp"), none},
		{b("aix-ppc64", "term"), none},
		{b("aix-ppc64@go1.12", "go"), onlyPost},
		{b("aix-ppc64@go1.12", "net"), none},
		{b("aix-ppc64@go1.12", "mobile"), none},
		{b("aix-ppc64@go1.13", "net"), onlyPost},
		{b("aix-ppc64@go1.13", "mobile"), none},
		{b("aix-ppc64@go1.11", "go"), none},
		{b("aix-ppc64@go1.11", "net"), none},
		{b("aix-ppc64@go1.11", "mobile"), none},

		// Illumos starts at Go 1.13
		{b("illumos-amd64-joyent", "go"), onlyPost},
		{b("illumos-amd64-joyent", "net"), onlyPost},
		{b("illumos-amd64-joyent", "sys"), onlyPost},
		{b("illumos-amd64-joyent@1.13", "go"), onlyPost},
		{b("illumos-amd64-joyent@1.12", "go"), none},
		{b("illumos-amd64-joyent@1.12", "sys"), none},
		{b("illumos-amd64-joyent@1.11", "go"), none},
		{b("illumos-amd64-joyent@1.11", "sys"), none},

		{b("linux-amd64-nocgo", "mobile"), none},

		// Virtual mobiledevices
		{b("darwin-arm64-corellium", "go"), isBuilder},
		{b("android-arm64-corellium", "go"), isBuilder},
		{b("android-arm-corellium", "go"), isBuilder},

		// Mobile builders that run with GOOS=linux/darwin and have
		// a device attached.
		{b("linux-amd64-androidemu", "mobile"), both},

		// But the emulators run all:
		{b("android-amd64-emu", "mobile"), isBuilder},
		{b("android-386-emu", "mobile"), isBuilder},
		{b("android-amd64-emu", "net"), isBuilder},
		{b("android-386-emu", "net"), isBuilder},
		{b("android-amd64-emu", "go"), isBuilder},
		{b("android-386-emu", "go"), isBuilder},

		{b("nacl-386", "go"), onlyPost},
		{b("nacl-386", "net"), none},
		{b("nacl-amd64p32", "go"), both},
		{b("nacl-amd64p32", "net"), none},

		// Only test tip for js/wasm, and only for some repos:
		{b("js-wasm", "go"), both},
		{b("js-wasm", "arch"), onlyPost},
		{b("js-wasm", "crypto"), onlyPost},
		{b("js-wasm", "sys"), onlyPost},
		{b("js-wasm", "net"), onlyPost},
		{b("js-wasm@go1.12", "net"), none},
		{b("js-wasm", "benchmarks"), none},
		{b("js-wasm", "debug"), none},
		{b("js-wasm", "mobile"), none},
		{b("js-wasm", "perf"), none},
		{b("js-wasm", "talks"), none},
		{b("js-wasm", "tools"), none},
		{b("js-wasm", "tour"), none},
		{b("js-wasm", "website"), none},

		// Race builders. Linux for all, GCE buidlers for
		// post-submit, and only post-submit for "go" for
		// Darwin (limited resources).
		{b("linux-amd64-race", "go"), both},
		{b("linux-amd64-race", "net"), both},
		{b("windows-amd64-race", "go"), onlyPost},
		{b("windows-amd64-race", "net"), onlyPost},
		{b("freebsd-amd64-race", "go"), onlyPost},
		{b("freebsd-amd64-race", "net"), onlyPost},
		{b("darwin-amd64-race", "go"), onlyPost},
		{b("darwin-amd64-race", "net"), none},

		// Long test.
		{b("linux-amd64-longtest", "go"), onlyPost},
		{b("linux-amd64-longtest", "net"), onlyPost},
		{b("linux-amd64-longtest@go1.12", "go"), onlyPost},
		{b("linux-amd64-longtest@go1.12", "net"), none},

		// Experimental exp repo runs in very few places.
		{b("linux-amd64", "exp"), both},
		{b("linux-amd64-race", "exp"), both},
		{b("linux-amd64-longtest", "exp"), onlyPost},
		{b("windows-386-2008", "exp"), both},
		{b("windows-amd64-2016", "exp"), both},
		{b("darwin-amd64-10_12", "exp"), onlyPost},
		{b("darwin-amd64-10_14", "exp"), onlyPost},
		// ... but not on most others:
		{b("freebsd-386-11_2", "exp"), none},
		{b("freebsd-386-12_0", "exp"), none},
		{b("freebsd-amd64-11_2", "exp"), none},
		{b("freebsd-amd64-12_0", "exp"), none},
		{b("openbsd-amd64-62", "exp"), none},
		{b("openbsd-amd64-64", "exp"), none},
		{b("js-wasm", "exp"), none},

		// exp is experimental; it doesn't test against release branches.
		{b("linux-amd64@go1.11", "exp"), none},
		{b("linux-amd64@go1.12", "exp"), none},

		// Only use latest macOS for subrepos, and only amd64:
		{b("darwin-amd64-10_12", "net"), onlyPost},
		{b("darwin-amd64-10_12@go1.11", "net"), onlyPost},
		{b("darwin-amd64-10_11", "net"), none},
		{b("darwin-amd64-10_11@go1.11", "net"), none},
		{b("darwin-amd64-10_11@go1.12", "net"), none},
		{b("darwin-386-10_14@go1.11", "net"), none},

		{b("darwin-amd64-10_14", "go"), onlyPost},
		{b("darwin-amd64-10_12", "go"), onlyPost},
		{b("darwin-amd64-10_11", "go"), onlyPost},
		{b("darwin-amd64-10_10", "go"), none},
		{b("darwin-amd64-10_10@go1.12", "go"), onlyPost},
		{b("darwin-amd64-10_10@go1.11", "go"), onlyPost},
		{b("darwin-386-10_14", "go"), onlyPost},
		{b("darwin-386-10_14@go1.12", "go"), onlyPost},
		{b("darwin-386-10_14@go1.11", "go"), onlyPost},

		// plan9 only lived at master. We didn't support any past releases.
		// But it's off for now as it's always failing.
		{b("plan9-386", "go"), none},  // temporarily disabled
		{b("plan9-386", "net"), none}, // temporarily disabled
		{b("plan9-386@go1.11", "go"), none},
		{b("plan9-386@go1.12", "go"), none},
		{b("plan9-386@go1.11", "net"), none},
		{b("plan9-386@go1.12", "net"), none},
		{b("plan9-amd64-9front", "go"), onlyPost},
		{b("plan9-amd64-9front@go1.11", "go"), none},
		{b("plan9-amd64-9front@go1.12", "go"), none},
		{b("plan9-amd64-9front", "net"), onlyPost},
		{b("plan9-amd64-9front@go1.11", "net"), none},
		{b("plan9-amd64-9front@go1.12", "net"), none},
		{b("plan9-arm", "go"), onlyPost},
		{b("plan9-arm@go1.11", "go"), none},
		{b("plan9-arm@go1.12", "go"), none},
		{b("plan9-arm", "net"), onlyPost},
		{b("plan9-arm@go1.11", "net"), none},
		{b("plan9-arm@go1.12", "net"), none},

		// x/net master with Go 1.11 doesn't work on our builders
		// on 32-bit FreeBSD. Remove distracting red from the dashboard
		// that'll never be fixed.
		{b("freebsd-386-11_2@go1.11", "net"), none},
		{b("freebsd-386-12_0@go1.11", "net"), none},
	}
	for _, tt := range tests {
		t.Run(tt.br.testName, func(t *testing.T) {
			bc, ok := Builders[tt.br.builder]
			if !ok {
				t.Fatalf("unknown builder %q", tt.br.builder)
			}
			gotPost := bc.BuildsRepoPostSubmit(tt.br.repo, tt.br.branch, tt.br.goBranch)
			if tt.want&isBuilder != 0 && !gotPost {
				t.Errorf("not a post-submit builder, but expected")
			}
			if tt.want&notBuilder != 0 && gotPost {
				t.Errorf("unexpectedly a post-submit builder")
			}

			gotTry := bc.BuildsRepoTryBot(tt.br.repo, tt.br.branch, tt.br.goBranch)
			if tt.want&isTrybot != 0 && !gotTry {
				t.Errorf("not trybot, but expected")
			}
			if tt.want&notTrybot != 0 && gotTry {
				t.Errorf("unexpectedly a trybot")
			}

			if t.Failed() {
				t.Logf("For: %+v", tt.br)
			}
		})
	}
}

func TestHostConfigsAllUsed(t *testing.T) {
	used := map[string]bool{}
	for _, conf := range Builders {
		used[conf.HostType] = true
	}
	for hostType := range Hosts {
		if !used[hostType] {
			// Currently host-linux-armhf-cross and host-linux-armel-cross aren't
			// referenced, but the coordinator hard-codes them, so don't make
			// this an error for now.
			t.Logf("warning: host type %q is not referenced from any build config", hostType)
		}
	}
}

// tests that goBranch is optional for repo == "go"
func TestBuildsRepoAtAllImplicitGoBranch(t *testing.T) {
	builder := Builders["android-amd64-emu"]
	got := builder.buildsRepoAtAll("go", "master", "")
	if !got {
		t.Error("got = false; want true")
	}
}

func TestShouldRunDistTest(t *testing.T) {
	type buildMode int
	const (
		tryMode    buildMode = 0
		postSubmit buildMode = 1
	)

	tests := []struct {
		builder string
		test    string
		mode    buildMode
		want    bool
	}{
		{"linux-amd64", "api", postSubmit, true},
		{"linux-amd64", "api", tryMode, true},

		{"linux-amd64", "reboot", tryMode, true},
		{"linux-amd64-race", "reboot", tryMode, false},

		{"darwin-amd64-10_10", "test:foo", postSubmit, false},
		{"darwin-amd64-10_11", "test:foo", postSubmit, false},
		{"darwin-amd64-10_12", "test:foo", postSubmit, false},
		{"darwin-amd64-10_14", "test:foo", postSubmit, false},
		{"darwin-amd64-10_14", "test:foo", postSubmit, false},
		{"darwin-amd64-10_14", "reboot", postSubmit, false},
		{"darwin-amd64-10_14", "api", postSubmit, false},
		{"darwin-amd64-10_14", "codewalk", postSubmit, false},
	}
	for _, tt := range tests {
		bc, ok := Builders[tt.builder]
		if !ok {
			t.Errorf("unknown builder %q", tt.builder)
			continue
		}
		isTry := tt.mode == tryMode
		if isTry && !bc.BuildsRepoTryBot("go", "master", "master") {
			t.Errorf("builder %q is not a trybot, so can't run test %q in try mode", tt.builder, tt.test)
			continue
		}
		got := bc.ShouldRunDistTest(tt.test, isTry)
		if got != tt.want {
			t.Errorf("%q.ShouldRunDistTest(%q, try %v) = %v; want %v", tt.builder, tt.test, isTry, got, tt.want)
		}
	}
}
