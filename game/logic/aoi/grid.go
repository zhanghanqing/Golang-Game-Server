// Package aoi implements Area Of Interest management with a uniform grid
// (the "nine-grid" algorithm).
//
// The map is divided into square cells. An entity sees every entity located in
// its own cell and the 8 surrounding cells. When an entity moves to another
// cell we compute which watchers entered or left its view, so the scene only
// sends updates to players that can actually see each other. This turns the
// O(N^2) "broadcast to everyone" into O(N * density).
//
// Grid is pure data and not safe for concurrent use; the scene actor owns it.
package aoi

// Grid tracks entity positions on a uniform grid.
type Grid struct {
	cellSize float32
	cols     int
	rows     int
	cells    []map[int64]struct{}
	where    map[int64]int // entity -> cell index
}

// NewGrid creates a grid covering [0,width) x [0,height).
func NewGrid(width, height, cellSize float32) *Grid {
	cols := int(width/cellSize) + 1
	rows := int(height/cellSize) + 1
	cells := make([]map[int64]struct{}, cols*rows)
	for i := range cells {
		cells[i] = make(map[int64]struct{})
	}
	return &Grid{cellSize: cellSize, cols: cols, rows: rows, cells: cells, where: make(map[int64]int)}
}

func (g *Grid) cellOf(x, y float32) int {
	cx := clamp(int(x/g.cellSize), 0, g.cols-1)
	cy := clamp(int(y/g.cellSize), 0, g.rows-1)
	return cy*g.cols + cx
}

func clamp(v, lo, hi int) int {
	return min(max(v, lo), hi)
}

// around returns the ids in the 3x3 block centered on cell, excluding self.
func (g *Grid) around(cell int, self int64) map[int64]struct{} {
	cx, cy := cell%g.cols, cell/g.cols
	out := make(map[int64]struct{})
	for dy := -1; dy <= 1; dy++ {
		for dx := -1; dx <= 1; dx++ {
			x, y := cx+dx, cy+dy
			if x < 0 || y < 0 || x >= g.cols || y >= g.rows {
				continue
			}
			for id := range g.cells[y*g.cols+x] {
				if id != self {
					out[id] = struct{}{}
				}
			}
		}
	}
	return out
}

// Add inserts id at (x, y) and returns the entities that can now see it (and it them).
func (g *Grid) Add(id int64, x, y float32) []int64 {
	if _, ok := g.where[id]; ok {
		g.Remove(id)
	}
	c := g.cellOf(x, y)
	g.cells[c][id] = struct{}{}
	g.where[id] = c
	return keys(g.around(c, id))
}

// Remove deletes id and returns the entities that could see it.
func (g *Grid) Remove(id int64) []int64 {
	c, ok := g.where[id]
	if !ok {
		return nil
	}
	watchers := keys(g.around(c, id))
	delete(g.cells[c], id)
	delete(g.where, id)
	return watchers
}

// Move relocates id and returns:
//
//	entered: entities that become mutually visible
//	left:    entities that are no longer mutually visible
//	stayed:  entities visible both before and after the move
func (g *Grid) Move(id int64, x, y float32) (entered, left, stayed []int64) {
	oldCell, ok := g.where[id]
	if !ok {
		return g.Add(id, x, y), nil, nil
	}
	newCell := g.cellOf(x, y)
	if oldCell == newCell {
		return nil, nil, keys(g.around(oldCell, id))
	}
	before := g.around(oldCell, id)
	delete(g.cells[oldCell], id)
	g.cells[newCell][id] = struct{}{}
	g.where[id] = newCell
	after := g.around(newCell, id)

	for o := range after {
		if _, ok := before[o]; ok {
			stayed = append(stayed, o)
		} else {
			entered = append(entered, o)
		}
	}
	for o := range before {
		if _, ok := after[o]; !ok {
			left = append(left, o)
		}
	}
	return entered, left, stayed
}

// Neighbors returns the entities visible to id.
func (g *Grid) Neighbors(id int64) []int64 {
	c, ok := g.where[id]
	if !ok {
		return nil
	}
	return keys(g.around(c, id))
}

// Len returns the number of entities.
func (g *Grid) Len() int { return len(g.where) }

func keys(m map[int64]struct{}) []int64 {
	out := make([]int64, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
