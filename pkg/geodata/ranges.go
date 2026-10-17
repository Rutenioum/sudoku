/*
Copyright (C) 2026 by saba <contact me via issue>

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program. If not, see <http://www.gnu.org/licenses/>.

In addition, no derivative work may use the name or imply association
with this application without prior consent.
*/
package geodata

import (
	"bytes"
	"encoding/binary"
	"net"
	"sort"
)

// IPRange represents an IP interval [Start, End]
type IPRange struct {
	Start uint32
	End   uint32
}

type IPv6Range struct {
	Start [16]byte
	End   [16]byte
}

func ipToUint32(ip net.IP) uint32 {
	if len(ip) == 16 {
		return binary.BigEndian.Uint32(ip[12:16])
	}
	return binary.BigEndian.Uint32(ip)
}

func containsIPv4(ranges []IPRange, val uint32) bool {
	idx := sort.Search(len(ranges), func(i int) bool { return ranges[i].End >= val })
	return idx < len(ranges) && ranges[idx].Start <= val
}

func containsIPv6(ranges []IPv6Range, key [16]byte) bool {
	idx := sort.Search(len(ranges), func(i int) bool { return compareIPv6(ranges[i].End, key) >= 0 })
	return idx < len(ranges) && compareIPv6(ranges[idx].Start, key) <= 0
}

func mergeRanges(ranges []IPRange) []IPRange {
	if len(ranges) == 0 {
		return nil
	}
	sort.Slice(ranges, func(i, j int) bool {
		return ranges[i].Start < ranges[j].Start
	})
	var result []IPRange
	current := ranges[0]
	for i := 1; i < len(ranges); i++ {
		next := ranges[i]
		if current.End >= next.Start-1 {
			if next.End > current.End {
				current.End = next.End
			}
		} else {
			result = append(result, current)
			current = next
		}
	}
	result = append(result, current)
	return result
}

func mergeIPv6Ranges(ranges []IPv6Range) []IPv6Range {
	if len(ranges) == 0 {
		return nil
	}
	sort.Slice(ranges, func(i, j int) bool {
		return compareIPv6(ranges[i].Start, ranges[j].Start) < 0
	})
	result := make([]IPv6Range, 0, len(ranges))
	current := ranges[0]
	for i := 1; i < len(ranges); i++ {
		next := ranges[i]
		if compareIPv6(current.End, next.Start) >= 0 {
			if compareIPv6(next.End, current.End) > 0 {
				current.End = next.End
			}
		} else {
			result = append(result, current)
			current = next
		}
	}
	result = append(result, current)
	return result
}

func compareIPv6(a, b [16]byte) int {
	return bytes.Compare(a[:], b[:])
}
