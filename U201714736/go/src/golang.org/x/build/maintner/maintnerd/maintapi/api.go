// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package maintapi exposes a gRPC maintner service for a given corpus.
package maintapi

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/build/gerrit"
	"golang.org/x/build/maintner"
	"golang.org/x/build/maintner/maintnerd/apipb"
	"golang.org/x/build/maintner/maintnerd/maintapi/version"
)

// NewAPIService creates a gRPC Server that serves the Maintner API for the given corpus.
func NewAPIService(corpus *maintner.Corpus) apipb.MaintnerServiceServer {
	return apiService{corpus}
}

// apiService implements apipb.MaintnerServiceServer using the Corpus c.
type apiService struct {
	c *maintner.Corpus
	// There really shouldn't be any more fields here.
	// All state should be in c.
	// A bool like "in staging" should just be a global flag.
}

func (s apiService) HasAncestor(ctx context.Context, req *apipb.HasAncestorRequest) (*apipb.HasAncestorResponse, error) {
	if len(req.Commit) != 40 {
		return nil, errors.New("invalid Commit")
	}
	if len(req.Ancestor) != 40 {
		return nil, errors.New("invalid Ancestor")
	}
	s.c.RLock()
	defer s.c.RUnlock()

	commit := s.c.GitCommit(req.Commit)
	res := new(apipb.HasAncestorResponse)
	if commit == nil {
		// TODO: wait for it? kick off a fetch of it and then answer?
		// optional?
		res.UnknownCommit = true
		return res, nil
	}
	if a := s.c.GitCommit(req.Ancestor); a != nil {
		res.HasAncestor = commit.HasAncestor(a)
	}
	return res, nil
}

func isStagingCommit(cl *maintner.GerritCL) bool {
	return cl.Commit != nil &&
		strings.Contains(cl.Commit.Msg, "DO NOT SUBMIT") &&
		strings.Contains(cl.Commit.Msg, "STAGING")
}

func tryBotStatus(cl *maintner.GerritCL, forStaging bool) (try, done bool) {
	if cl.Commit == nil {
		return // shouldn't happen
	}
	if forStaging != isStagingCommit(cl) {
		return
	}
	for _, msg := range cl.Messages {
		if msg.Version != cl.Version {
			continue
		}
		firstLine := msg.Message
		if nl := strings.IndexByte(firstLine, '\n'); nl != -1 {
			firstLine = firstLine[:nl]
		}
		if !strings.Contains(firstLine, "TryBot") {
			continue
		}
		if strings.Contains(firstLine, "Run-TryBot+1") {
			try = true
		}
		if strings.Contains(firstLine, "-Run-TryBot") {
			try = false
		}
		if strings.Contains(firstLine, "TryBot-Result") {
			done = true
		}
	}
	return
}

func tryWorkItem(cl *maintner.GerritCL) *apipb.GerritTryWorkItem {
	return &apipb.GerritTryWorkItem{
		Project:  cl.Project.Project(),
		Branch:   strings.TrimPrefix(cl.Branch(), "refs/heads/"),
		ChangeId: cl.ChangeID(),
		Commit:   cl.Commit.Hash.String(),
	}
}

func (s apiService) GetRef(ctx context.Context, req *apipb.GetRefRequest) (*apipb.GetRefResponse, error) {
	s.c.RLock()
	defer s.c.RUnlock()
	gp := s.c.Gerrit().Project(req.GerritServer, req.GerritProject)
	if gp == nil {
		return nil, errors.New("unknown gerrit project")
	}
	res := new(apipb.GetRefResponse)
	hash := gp.Ref(req.Ref)
	if hash != "" {
		res.Value = hash.String()
	}
	return res, nil
}

var tryCache struct {
	sync.Mutex
	forNumChanges int       // number of label changes in project val is valid for
	lastPoll      time.Time // of gerrit
	val           *apipb.GoFindTryWorkResponse
}

var tryBotGerrit = gerrit.NewClient("https://go-review.googlesource.com", gerrit.NoAuth)

func (s apiService) GoFindTryWork(ctx context.Context, req *apipb.GoFindTryWorkRequest) (*apipb.GoFindTryWorkResponse, error) {
	tryCache.Lock()
	defer tryCache.Unlock()

	s.c.RLock()
	defer s.c.RUnlock()

	// Count the number of vote label changes over time. If it's
	// the same as the last query, return a cached result without
	// hitting Gerrit.
	var sumChanges int
	s.c.Gerrit().ForeachProjectUnsorted(func(gp *maintner.GerritProject) error {
		if gp.Server() != "go.googlesource.com" {
			return nil
		}
		sumChanges += gp.NumLabelChanges()
		return nil
	})

	now := time.Now()
	const maxPollInterval = 15 * time.Second

	if tryCache.val != nil &&
		(tryCache.forNumChanges == sumChanges ||
			tryCache.lastPoll.After(now.Add(-maxPollInterval))) {
		return tryCache.val, nil
	}

	tryCache.lastPoll = now

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	const query = "label:Run-TryBot=1 label:TryBot-Result=0 status:open"
	cis, err := tryBotGerrit.QueryChanges(ctx, query, gerrit.QueryChangesOpt{
		Fields: []string{"CURRENT_REVISION", "CURRENT_COMMIT"},
	})
	if err != nil {
		return nil, err
	}
	tryCache.forNumChanges = sumChanges

	goProj := s.c.Gerrit().Project("go.googlesource.com", "go")
	supportedReleases, err := supportedGoReleases(goProj)
	if err != nil {
		return nil, err
	}

	res := new(apipb.GoFindTryWorkResponse)
	for _, ci := range cis {
		cl := s.c.Gerrit().Project("go.googlesource.com", ci.Project).CL(int32(ci.ChangeNumber))
		if cl == nil {
			log.Printf("nil Gerrit CL %v", ci.ChangeNumber)
			continue
		}
		work := tryWorkItem(cl)
		if ci.CurrentRevision != "" {
			// In case maintner is behind.
			work.Commit = ci.CurrentRevision
		}
		if work.Project == "go" {
			// Trybot on Go repo. Set the GoVersion field based on branch name.
			if work.Branch == "master" {
				latest := supportedReleases[0]
				work.GoVersion = []*apipb.MajorMinor{{latest.Major, latest.Minor}}
			} else if major, minor, ok := parseReleaseBranchVersion(work.Branch); ok {
				// A release branch like release-branch.goX.Y.
				// Use the major-minor Go version determined from the branch name.
				work.GoVersion = []*apipb.MajorMinor{{major, minor}}
			} else {
				// A branch that is neither master nor release-branch.goX.Y.
				// I don't see a straightforward way to compute its version,
				// so use the latest Go release until we need to do more.
				latest := supportedReleases[0]
				work.GoVersion = []*apipb.MajorMinor{{latest.Major, latest.Minor}}
			}
		} else {
			// Trybot on a subrepo. Set the Go fields to master and the supported releases.
			work.GoCommit = []string{goProj.Ref("refs/heads/master").String()}
			work.GoBranch = []string{"master"}
			latest := supportedReleases[0]
			work.GoVersion = []*apipb.MajorMinor{{latest.Major, latest.Minor}}
			for _, r := range supportedReleases {
				work.GoCommit = append(work.GoCommit, r.BranchCommit)
				work.GoBranch = append(work.GoBranch, r.BranchName)
				work.GoVersion = append(work.GoVersion, &apipb.MajorMinor{r.Major, r.Minor})
			}
		}
		res.Waiting = append(res.Waiting, work)
	}

	// Sort in some stable order.
	//
	// TODO: better would be sorting by time the trybot was
	// requested, or the time of the CL. But we don't return that
	// (yet?) because the coordinator has never needed it
	// historically. But if we do a proper scheduler (Issue
	// 19178), perhaps it would be good data to have in the
	// coordinator.
	sort.Slice(res.Waiting, func(i, j int) bool {
		return res.Waiting[i].Commit < res.Waiting[j].Commit
	})
	tryCache.val = res

	log.Printf("maintnerd: GetTryWork: for label changes of %d, cached %d trywork items.",
		sumChanges, len(res.Waiting))

	return res, nil
}

// parseTagVersion parses the major-minor-patch version triplet
// from goX, goX.Y, or goX.Y.Z tag names,
// and reports whether the tag name is valid.
//
// Tags with suffixes like "go1.2beta3" or "go1.2rc1" are rejected.
//
// For example, "go1" is parsed as version 1.0.0,
// "go1.2" is parsed as version 1.2.0,
// and "go1.2.3" is parsed as version 1.2.3.
func parseTagVersion(tagName string) (major, minor, patch int32, ok bool) {
	maj, min, pat, ok := version.ParseTag(tagName)
	return int32(maj), int32(min), int32(pat), ok
}

// parseReleaseBranchVersion parses the major-minor version pair
// from release-branch.goX or release-branch.goX.Y release branch names,
// and reports whether the release branch name is valid.
//
// For example, "release-branch.go1" is parsed as version 1.0,
// and "release-branch.go1.2" is parsed as version 1.2.
func parseReleaseBranchVersion(branchName string) (major, minor int32, ok bool) {
	maj, min, ok := version.ParseReleaseBranch(branchName)
	return int32(maj), int32(min), ok
}

// ListGoReleases lists Go releases. A release is considered to exist
// if a tag for it exists.
func (s apiService) ListGoReleases(ctx context.Context, req *apipb.ListGoReleasesRequest) (*apipb.ListGoReleasesResponse, error) {
	s.c.RLock()
	defer s.c.RUnlock()
	goProj := s.c.Gerrit().Project("go.googlesource.com", "go")
	releases, err := supportedGoReleases(goProj)
	if err != nil {
		return nil, err
	}
	return &apipb.ListGoReleasesResponse{
		Releases: releases,
	}, nil
}

// nonChangeRefLister is implemented by *maintner.GerritProject,
// or something that acts like it for testing.
type nonChangeRefLister interface {
	// ForeachNonChangeRef calls fn for each git ref on the server that is
	// not a change (code review) ref. In general, these correspond to
	// submitted changes. fn is called serially with sorted ref names.
	// Iteration stops with the first non-nil error returned by fn.
	ForeachNonChangeRef(fn func(ref string, hash maintner.GitHash) error) error
}

// supportedGoReleases returns the latest patches of releases that are
// considered supported per policy. Sorted by version with latest first.
// The returned list will be empty if and only if the error is non-nil.
func supportedGoReleases(goProj nonChangeRefLister) ([]*apipb.GoRelease, error) {
	type majorMinor struct {
		Major, Minor int32
	}
	type tag struct {
		Patch  int32
		Name   string
		Commit maintner.GitHash
	}
	type branch struct {
		Name   string
		Commit maintner.GitHash
	}
	tags := make(map[majorMinor]tag)
	branches := make(map[majorMinor]branch)

	// Iterate over Go tags and release branches. Find the latest patch
	// for each major-minor pair, and fill in the appropriate fields.
	err := goProj.ForeachNonChangeRef(func(ref string, hash maintner.GitHash) error {
		switch {
		case strings.HasPrefix(ref, "refs/tags/go"):
			// Tag.
			tagName := ref[len("refs/tags/"):]
			major, minor, patch, ok := parseTagVersion(tagName)
			if !ok {
				return nil
			}
			if t, ok := tags[majorMinor{major, minor}]; ok && patch <= t.Patch {
				// This patch version is not newer than what we've already seen, skip it.
				return nil
			}
			tags[majorMinor{major, minor}] = tag{
				Patch:  patch,
				Name:   tagName,
				Commit: hash,
			}

		case strings.HasPrefix(ref, "refs/heads/release-branch.go"):
			// Release branch.
			branchName := ref[len("refs/heads/"):]
			major, minor, ok := parseReleaseBranchVersion(branchName)
			if !ok {
				return nil
			}
			branches[majorMinor{major, minor}] = branch{
				Name:   branchName,
				Commit: hash,
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// A release is considered to exist for each git tag named "goX", "goX.Y", or "goX.Y.Z",
	// as long as it has a corresponding "release-branch.goX" or "release-branch.goX.Y" release branch.
	var rs []*apipb.GoRelease
	for v, t := range tags {
		b, ok := branches[v]
		if !ok {
			// In the unlikely case a tag exists but there's no release branch for it,
			// don't consider it a release. This way, callers won't have to do this work.
			continue
		}
		rs = append(rs, &apipb.GoRelease{
			Major:        v.Major,
			Minor:        v.Minor,
			Patch:        t.Patch,
			TagName:      t.Name,
			TagCommit:    t.Commit.String(),
			BranchName:   b.Name,
			BranchCommit: b.Commit.String(),
		})
	}

	// Sort by version. Latest first.
	sort.Slice(rs, func(i, j int) bool {
		x1, y1, z1 := rs[i].Major, rs[i].Minor, rs[i].Patch
		x2, y2, z2 := rs[j].Major, rs[j].Minor, rs[j].Patch
		if x1 != x2 {
			return x1 > x2
		}
		if y1 != y2 {
			return y1 > y2
		}
		return z1 > z2
	})

	// Per policy, only the latest two releases are considered supported.
	// Return an error if there aren't at least two releases, so callers
	// don't have to check for empty list.
	if len(rs) < 2 {
		return nil, fmt.Errorf("there was a problem finding supported Go releases")
	}
	return rs[:2], nil
}
