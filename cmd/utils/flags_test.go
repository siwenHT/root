// Copyright 2019 The go-ethereum Authors
// This file is part of go-ethereum.
//
// go-ethereum is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// go-ethereum is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with go-ethereum. If not, see <http://www.gnu.org/licenses/>.

// Package utils contains internal helper functions for go-ethereum commands.
package utils

import (
	"reflect"
	"testing"
)

func Test_SplitTagsFlag(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args string
		want map[string]string
	}{
		{
			"2 tags case",
			"host=localhost,bzzkey=123",
			map[string]string{
				"host":   "localhost",
				"bzzkey": "123",
			},
		},
		{
			"1 tag case",
			"host=localhost123",
			map[string]string{
				"host": "localhost123",
			},
		},
		{
			"empty case",
			"",
			map[string]string{},
		},
		{
			"garbage",
			"smth=smthelse=123",
			map[string]string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := SplitTagsFlag(tt.args); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("splitTagsFlag() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCacheAllowanceMB(t *testing.T) {
	t.Parallel()
	const mib = 1024 * 1024
	tests := []struct {
		totalBytes uint64
		want       int
	}{
		{32768 * mib, 10922},
		{64000 * mib, 21333},
		{65536 * mib, 39321},
		{192324 * mib, 115394},
	}
	for _, tc := range tests {
		if got := cacheAllowanceMB(tc.totalBytes); got != tc.want {
			t.Errorf("cacheAllowanceMB(%d MiB) = %d, want %d", tc.totalBytes/mib, got, tc.want)
		}
	}
}
