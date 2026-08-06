package simdjson

// Go's embedded-field dominance, as encoding/json's typeFields applies it:
// for each JSON name, the field at the shallowest embedding depth wins; if
// several share that depth, a single tagged one wins; otherwise they
// annihilate and NONE is encoded or matched. Both the encoder's field table
// and the decoder's plan collect every reachable field naively and filter
// through here -- stdlib's TestMarshalEmbeds caught the encoder emitting
// DUPLICATE KEYS for the fields these rules suppress, and the decoder's
// map-overwrite was the same algorithm wrong way round.

import "sort"

// domField is the neutral description the filter needs.
type domField struct {
	name   string
	depth  int
	tagged bool
	ord    int // collection order; survivors keep it
}

// dominantOrds returns the collection orders of the surviving fields, in
// their original order.
func dominantOrds(fields []domField) map[int]bool {
	byName := map[string][]domField{}
	for _, f := range fields {
		byName[f.name] = append(byName[f.name], f)
	}
	keep := map[int]bool{}
	for _, group := range byName {
		if len(group) == 1 {
			keep[group[0].ord] = true
			continue
		}
		sort.SliceStable(group, func(i, j int) bool {
			if group[i].depth != group[j].depth {
				return group[i].depth < group[j].depth
			}
			if group[i].tagged != group[j].tagged {
				return group[i].tagged
			}
			return false
		})
		// The shallowest depth's candidates decide everything.
		minDepth := group[0].depth
		var atMin []domField
		for _, f := range group {
			if f.depth == minDepth {
				atMin = append(atMin, f)
			}
		}
		switch {
		case len(atMin) == 1:
			keep[atMin[0].ord] = true
		default:
			tagged := 0
			var winner domField
			for _, f := range atMin {
				if f.tagged {
					tagged++
					winner = f
				}
			}
			if tagged == 1 {
				keep[winner.ord] = true
			}
			// Zero or several tagged at the same depth: annihilation --
			// nothing is kept, exactly as stdlib drops conflicting fields.
		}
	}
	return keep
}
