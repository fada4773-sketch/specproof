package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"
)

// style colors the output on a terminal; with NO_COLOR, TERM=dumb or
// output into a file or pipe it writes plain text.
type style struct{ on bool }

func newStyle(out io.Writer) style {
	f, ok := out.(*os.File)
	if !ok || os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return style{}
	}
	fi, err := f.Stat()
	return style{on: err == nil && fi.Mode()&os.ModeCharDevice != 0}
}

// ANSI colors.
const (
	red    = "31"
	green  = "32"
	yellow = "33"
	blue   = "34"
	cyan   = "36"
	dim    = "2"
	bold   = "1"
)

func (s style) paint(color, text string) string {
	if !s.on || color == "" || text == "" {
		return text
	}
	return "\x1b[" + color + "m" + text + "\x1b[0m"
}

// width is the width of the terminal: $COLUMNS, else 120.
func width() int {
	if n, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && n >= 60 {
		return n
	}
	return 120
}

// cell is one cell of a table and its color.
type cell struct{ text, color string }

// table writes rows under a head, each column as wide as its widest cell;
// the last column wraps at the width of the terminal and its further
// lines start under it.
func table(out io.Writer, st style, head []string, rows [][]cell) {
	if len(rows) == 0 {
		return
	}
	n := len(head)
	w := make([]int, n)
	for i, h := range head {
		w[i] = utf8.RuneCountInString(h)
	}
	for _, r := range rows {
		for i := 0; i < n-1 && i < len(r); i++ {
			w[i] = max(w[i], utf8.RuneCountInString(r[i].text))
		}
	}
	indent := 2
	for _, x := range w[:n-1] {
		indent += x + 2
	}
	last := max(width()-indent, 30)
	line := func(cells []cell) {
		var b strings.Builder
		b.WriteString("  ")
		for i := 0; i < n-1; i++ {
			c := cells[i]
			b.WriteString(st.paint(c.color, c.text))
			b.WriteString(strings.Repeat(" ", w[i]-utf8.RuneCountInString(c.text)+2))
		}
		lines := wrap(cells[n-1].text, last)
		for i, l := range lines {
			if i > 0 {
				b.WriteString("\n" + strings.Repeat(" ", indent))
			}
			b.WriteString(st.paint(cells[n-1].color, l))
		}
		fmt.Fprintln(out, strings.TrimRight(b.String(), " "))
	}
	hc := make([]cell, n)
	for i, h := range head {
		hc[i] = cell{h, dim}
	}
	line(hc)
	for _, r := range rows {
		line(r)
	}
}

// wrap breaks a text into lines of at most w runes, at spaces; a word
// longer than that stays whole. Line breaks of the text are kept.
func wrap(text string, w int) []string {
	var out []string
	for _, para := range strings.Split(text, "\n") {
		line := ""
		for _, word := range strings.Fields(para) {
			switch {
			case line == "":
				line = word
			case utf8.RuneCountInString(line)+1+utf8.RuneCountInString(word) > w:
				out = append(out, line)
				line = word
			default:
				line += " " + word
			}
		}
		out = append(out, line)
	}
	return out
}

// section writes a heading.
func section(out io.Writer, st style, title string) {
	fmt.Fprintln(out)
	fmt.Fprintln(out, st.paint(bold, title))
}
