package filter_test

import (
	"testing"

	"github.com/SergeAx/scrutus/internal/config"
	"github.com/SergeAx/scrutus/internal/core"
	"github.com/SergeAx/scrutus/internal/filter"
)

func TestDefaultsExemptDirectivesButNotProse(t *testing.T) {
	cases := []struct {
		comment string
		exempt  bool
	}{
		{"#!/usr/bin/env python3", true},
		{"# -*- coding: utf-8 -*-", true},
		{"# coding=latin-1 is what the legacy feed sends", true},
		{"/*! jQuery v3.7.1 | (c) OpenJS Foundation */", true},
		{"// Copyright 2026 The scrutus Authors", true},
		{"// SPDX-License-Identifier: MIT", true},
		{"//# sourceMappingURL=app.js.map", true},
		{"// prettier-ignore the aligned matrix below", true},
		{"/* istanbul ignore next: unreachable */", true},
		{`/* webpackChunkName: "vendors" */`, true},
		{"# fmt: off until the table ends", true},
		{"# pyright: ignore[reportGeneralTypeIssues]", true},
		{"// Retry once: the gateway drops the first call after idling.", false},
		{"# Keep the order: callers depend on insertion order.", false},
	}

	var findings []core.Finding
	for i, tc := range cases {
		findings = append(findings, core.Finding{
			ID:          tc.comment,
			File:        "f",
			Kind:        core.KindInline,
			CommentText: tc.comment,
			Comment:     core.Span{Start: i, End: i + 1},
		})
	}
	kept, _ := filter.Apply(findings, nil, config.Defaults().Comments, nil)

	scored := map[string]bool{}
	for _, f := range kept {
		scored[f.CommentText] = true
	}
	for _, tc := range cases {
		if scored[tc.comment] == tc.exempt {
			t.Errorf("%q: exempt = %v, want %v", tc.comment, !scored[tc.comment], tc.exempt)
		}
	}
}
