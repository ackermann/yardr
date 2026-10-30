package web

import (
	"context"
	"fmt"
	"strconv"

	"yardr.dev/yardr/internal/store"
)

// The shell's left edge is footplate's rail: the yard's chip, the three
// mode tiles in the rail's order, and Settings at the foot, with the open
// mode's column of rows beside it. Each tile opens a page — Crew Talk,
// Decide the Line page, Lines the Yard panel, Settings the Config hub —
// and the column holds the pages the rail does not name. No page changes
// its address, its handler or its centre; the rail only changes how a
// person reaches them.

// railTile is one of the rail's tiles: a mode with its glyph, and the pill
// it carries where the mode waits on the person reading.
type railTile struct {
	Name string // crew, decide, lines, settings
	// Label is the mode's name under its glyph: Crew, Decide, Lines,
	// Settings.
	Label string
	// Path is the page the tile opens.
	Path string
	// Glyph is the tile's mark: lamp, semaphore, track, gear. The shell
	// draws each as inline SVG on footplate's 24-grid.
	Glyph string
	// Open is whether the page stands in this mode.
	Open bool
	// Pill is the count's numeral (2, 9+), "" where the tile carries none.
	Pill string
	// Title is the tile's title and aria-label: the mode, with the count
	// in words where a pill stands.
	Title string
}

// railRow is one row of the open mode's column.
type railRow struct {
	Label string
	Path  string
	// Current is whether the page stands on this row.
	Current bool
}

// railColumn is the open mode's column: its head and its rows.
type railColumn struct {
	// Head names the open mode, in the section face.
	Head string
	Rows []railRow
}

// rail is the shell's left edge: the yard's chip, the tiles, and the open
// mode's column.
type rail struct {
	// Yard is the yard's name, the chip's words.
	Yard string
	// Chip are its first two letters, on the chip.
	Chip string
	// Shade is the chip's rust shade, 0 to 2: the shell's chip-0 through
	// chip-2. A number, not a colour, because the page's policy refuses
	// inline style.
	Shade  int
	Tiles  []railTile
	Column *railColumn // nil where no mode is open (an error page)
}

// railModes are the rail's tiles in the rail's order, Settings at the foot.
var railModes = []railTile{
	{Name: "crew", Label: "Crew", Path: "/talk", Glyph: "lamp"},
	{Name: "decide", Label: "Decide", Path: "/line", Glyph: "semaphore"},
	{Name: "lines", Label: "Lines", Path: "/flows?panel=1", Glyph: "track"},
	{Name: "settings", Label: "Settings", Path: "/config", Glyph: "gear"},
}

// modeFor is the mode the page with menu entry name stands in: the tile the
// shell marks open and the column it shows. "" opens none (an error page).
func modeFor(name string) string {
	switch name {
	case "talk", "crew", "sessions":
		return "crew"
	case "home", "line", "previews":
		return "decide"
	case "yard", "flows", "beads":
		return "lines"
	case "config", "pairing":
		return "settings"
	}
	return ""
}

// columnRow is one row of a mode's column: the page's label and path, with
// the menu name the shell marks current by, "" where the row's own path
// marks it.
type columnRow struct {
	label, path, menu string
}

// railColumns are the rows of the three modes: each with the menu name its
// page carries. Settings' rows share one menu entry, so they are built per
// request instead (settingsRows).
var railColumns = map[string][]columnRow{
	"crew": {
		{"Talk", "/talk", "talk"},
		{"Crew", "/crew", "crew"},
		{"Sessions", "/sessions", "sessions"},
	},
	"decide": {
		{"Home", "/", "home"},
		{"Line", "/line", "line"},
		{"Previews", "/previews", "previews"},
	},
	"lines": {
		{"Yard", "/flows?panel=1", "yard"},
		{"Flows", "/flows", "flows"},
		{"Beads", "/beads", "beads"},
	},
}

// settingsRows is the Settings column: Config, then the hub's own lists,
// each with the filter its page carries. Telemetry stands for the operator
// alone, as on the hub; Pair device for a signed-in view alone, as the
// menu filtered it before the rail, and the handler still refuses any
// other view.
func (s *Server) settingsRows(ctx context.Context) []columnRow {
	rows := []columnRow{
		{"Config", "/config", ""},
		{"Depots", "/depots", ""},
		{"Groups", "/groups", ""},
		{"Routes", "/routes", ""},
		{"Peers", "/peers", ""},
		{"Deps", "/deps", ""},
		{"Limits", "/limits", ""},
	}
	if s.isOperator(ctx) {
		rows = append(rows, columnRow{"Telemetry", "/telemetry", ""})
	}
	if s.jwt != nil {
		rows = append(rows, columnRow{"Pair device", "/pairing", ""})
	}
	return rows
}

// columnFor is the open mode's column: its head and rows, with the current
// page marked. The three modes' rows mark by menu name; the Settings rows
// share one, so each marks by its own path. Nil where no mode is open.
func (s *Server) columnFor(ctx context.Context, menu, path string) *railColumn {
	mode := modeFor(menu)
	if mode == "" {
		return nil
	}
	rows := railColumns[mode]
	if mode == "settings" {
		rows = s.settingsRows(ctx)
	}
	out := &railColumn{Rows: make([]railRow, 0, len(rows))}
	for _, m := range railModes {
		if m.Name == mode {
			out.Head = m.Label
		}
	}
	for _, r := range rows {
		current := r.menu != "" && r.menu == menu || r.menu == "" && r.path == path
		out.Rows = append(out.Rows, railRow{Label: r.label, Path: r.path, Current: current})
	}
	return out
}

// pillLabel is the numeral a tile carries for n: 9+ above nine, as
// footplate's badge caps it.
func pillLabel(n int) string {
	if n > 9 {
		return "9+"
	}
	return strconv.Itoa(n)
}

// crewWords is Crew's count in words, for the tile's title and aria-label.
func crewWords(n int) string {
	if n == 1 {
		return "1 unread letter"
	}
	return fmt.Sprintf("%d unread letters", n)
}

// decideWords is Decide's count in words, for the tile's title and
// aria-label, in the header's words.
func decideWords(n int) string {
	if n == 1 {
		return "1 waits for you"
	}
	return fmt.Sprintf("%d wait for you", n)
}

// rail is the shell's left edge for the page: the yard's chip, the tiles
// with their pills, and the open mode's column. inbox is the acting user's
// inbox read, nil where the view acts as no user; l is the shell's line,
// nil on an error page. Crew's pill is the inbox's aggregate unread at
// this render — the web view acknowledges nothing, so it falls only when
// footplate, yardr chat or yardr inbox acknowledges the mail. Decide's is
// the line's beads carrying an open ask, one per bead however many asks it
// holds, held or not — never the shell's Needs number, which counts
// everything that stops the yard. Lines carries no pill: the web view
// keeps no per-person watermark to count its changed beads against. No
// pill stands at zero, and none stands on an error page.
func (s *Server) rail(ctx context.Context, menu, path string, l *Line, inbox *store.Mailbox) rail {
	yard := s.yardName(ctx)
	out := rail{Yard: yard, Chip: chipLetters(yard), Shade: yardSlot(yard)}
	open := modeFor(menu)
	decide := 0
	if l != nil {
		for _, w := range l.Waits {
			if w.HasAsk() {
				decide++
			}
		}
		for _, w := range l.Parked {
			if w.HasAsk() {
				decide++
			}
		}
	}
	for _, m := range railModes {
		t := m
		t.Open = m.Name == open
		t.Title = m.Label
		switch m.Name {
		case "crew":
			if inbox != nil && inbox.Unread > 0 {
				t.Pill = pillLabel(inbox.Unread)
				t.Title = m.Label + " — " + crewWords(inbox.Unread)
			}
		case "decide":
			if l != nil && decide > 0 {
				t.Pill = pillLabel(decide)
				t.Title = m.Label + " — " + decideWords(decide)
			}
		}
		out.Tiles = append(out.Tiles, t)
	}
	out.Column = s.columnFor(ctx, menu, path)
	return out
}

// chipLetters are the yard chip's letters: the yard name's first two, as
// footplate's chip carries them.
func chipLetters(yard string) string {
	r := []rune(yard)
	if len(r) > 2 {
		r = r[:2]
	}
	return string(r)
}

// yardSlot is the yard's chip shade: FNV-1a over its name, as footplate's
// slot hashes it (theme.rs), so the same yard is the same rust on both.
// The web view is dark in every case, so the slot is into the dark set
// alone (yard-300 through yard-500), whose letters are the dark ink.
func yardSlot(yard string) int {
	h := uint64(0xcbf29ce484222325)
	for i := 0; i < len(yard); i++ {
		h ^= uint64(yard[i])
		h *= 0x100000001b3
	}
	return int(h % 3)
}
