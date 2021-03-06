package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"sort"
)

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// Breaks apart a slice into multiple subslices, with limited number of items
// each.
func strSliceSplit(s []string, limit int) [][]string {
	if limit < 1 {
		panic("Non-positive limit")
	}

	var result [][]string

	for i := 0; i < len(s); i += limit {
		j := min(len(s), i+limit)
		result = append(result, s[i:j])
	}

	return result
}

// Deletes a filename without reporting back errors, if any.
func deleteFile(filename string) {
	fmt.Fprintf(os.Stderr, "Erasing temp file %v...\n", filename)
	if err := os.Remove(filename); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
	}
}

// Deletes multiple filenames without reporting back errors, if any.
func deleteFiles(filenames []string) {
	for _, filename := range filenames {
		deleteFile(filename)
	}
}

// A source represents sorted text file as a stack, where only the top line is
// available.
type source struct {
	top string
	r   *bufio.Reader
}

// Updates the top line with the next one from the file.
func (s *source) pop() error {
	var err error
	s.top, err = s.r.ReadString('\n')
	return err
}

// A sourceSet combines multiple sources into one sorted stack of lines.
type sourceSet map[*source]bool

func newSourceSet(rs []io.Reader) (sourceSet, error) {
	ss := make(sourceSet)

	for _, r := range rs {
		s := &source{"", bufio.NewReader(r)}
		err := s.pop()
		if err == io.EOF {
			continue
		}
		if err != nil {
			return nil, err
		}
		ss[s] = true
	}

	return ss, nil
}

// Returns the current minimum line in the whole sourceSet, removing it from the
// set.
func (ss sourceSet) popMin() (string, error) {
	var min *source
	first := true

	for s, _ := range ss {
		if first {
			min = s
			first = false
			continue
		}
		if s.top < min.top {
			min = s
		}
	}

	result := min.top

	// Advance the consumed source
	err := min.pop()
	if err == io.EOF {
		delete(ss, min)
		err = nil
	}

	return result, err
}

// A stringWriter is a writer that accepts strings. This is a common interface
// for bufio.Writer and bytes.Buffer, allowing interchangable use of one instead
// of the other.
type stringWriter interface {
	WriteString(s string) (int, error)
}

// Merges a set of sorted inputs into one sorted output.
func mergeSimple(rs []io.Reader, w stringWriter) error {
	// Initialize source set
	sources, err := newSourceSet(rs)
	if err != nil {
		return err
	}

	// Do merge
	for (len(sources) > 0) && (err == nil) {
		var min string
		min, err = sources.popMin()
		if err != nil {
			return err
		}
		_, err = w.WriteString(min)
	}

	return err
}

// Merges a set of sorted input files into one sorted output file.  After the
// merge input files are deleted.
func mergeSimpleFiles(names []string) (string, error) {

	// Schedule deletion of all input files
	defer func() {
		deleteFiles(names)
	}()

	// Create output file
	outf, err := ioutil.TempFile("", "filesort_merge_")
	if err != nil {
		return "", err
	}
	defer func() {
		outf.Close()
	}()
	fmt.Fprintf(os.Stderr, "Writing temp file %v...\n", outf.Name())
	out := bufio.NewWriter(outf)

	// Prepare all input files
	var files = make([]io.Reader, 0, len(names))
	for _, name := range names {
		var f *os.File
		f, err = os.Open(name)
		if err != nil {
			break
		}
		defer func() {
			f.Close()
		}()

		files = append(files, f)
	}

	if err == nil {
		err = mergeSimple(files, out)
	}
	if err == nil {
		err = out.Flush()
	}

	// If the merge fails, delete the output file
	if err != nil {
		defer func() {
			deleteFile(outf.Name())
		}()
	}

	return outf.Name(), err
}

// Merges a set of sorted input files into one sorted output file, using no more
// than limit number opened input files at a time.  After the merge the input
// files are deleted, unless it is only one file, in which case it is returned
// as result.
func merge(names []string, limit int) (string, error) {
	// Handle basic cases
	switch len(names) {
	case 0:
		panic("Empty names")
	case 1:
		return names[0], nil
	}

	// Simple merge when the number of files is within the limit
	if len(names) <= limit {
		return mergeSimpleFiles(names)
	}

	// Recursively reduce names to the limit
	name_groups := strSliceSplit(names, limit)
	reduced_names := make([]string, 0, len(name_groups))
	for _, group := range name_groups {
		name, err := merge(group, limit)
		if name != "" {
			reduced_names = append(reduced_names, name)
		}
		if err != nil {
			return "", err
		}
	}
	return merge(reduced_names, limit)
}

// Reads limit number of lines from a bufio.Reader.  If the last line does not
// end with a newline, it is automatically appended.
func readLines(r *bufio.Reader, limit int) ([]string, error) {
	lines := make([]string, 0, limit)
	var err error

	for (len(lines) < limit) && (err == nil) {
		var line string
		line, err = r.ReadString('\n')
		if err != nil {
			if err != io.EOF {
				break
			}
			if len(line) == 0 {
				// no data, skip this "line"
				break
			} else if line[len(line)-1] != '\n' {
				line = line + "\n"
			}
		}
		lines = append(lines, line)
	}

	return lines, err
}

// Writes an initial sorted chunk as temp file and returns its name.
func writeChunk(lines []string) (string, error) {
	f, err := ioutil.TempFile("", "filesort_chunk_")
	if err != nil {
		return "", err
	}
	defer func() {
		f.Close()
	}()

	name := f.Name()
	fmt.Fprintf(os.Stderr, "Writing temp file %v...\n", name)

	buf := bufio.NewWriter(f)
	for _, line := range lines {
		_, err = buf.WriteString(line)
		if err != nil {
			return name, err
		}
	}

	return name, buf.Flush()
}

// Splits an input io.Reader into a number of output chunk files, with no more
// than limit number of lines in each file.  Lines inside each chunk are sorted
// in memory.  Returns the created filenames.
func split(r io.Reader, limit int) ([]string, error) {
	in := bufio.NewReader(r)
	var chunk_names []string
	var err error

	for err == nil {
		var lines []string
		lines, err = readLines(in, limit)
		if (err != nil) && (err != io.EOF) {
			break
		}

		// Skip trailing empty chunks
		if (len(lines) == 0) && (len(chunk_names) > 0) {
			break
		}

		sort.Strings(lines)

		var name string
		if name, err = writeChunk(lines); name != "" {
			chunk_names = append(chunk_names, name)
		}
	}

	if err == io.EOF {
		err = nil
	}

	return chunk_names, err
}

// Reads and all lines from an input io.Reader and sorts them into an output
// file.  The sorting algorithm uses no more than limit number of lines at a
// time.  Returns the name of the sorted output file.
func sortLines(r io.Reader, limit int) (string, error) {
	names, err := split(r, limit)
	if err != nil {
		deleteFiles(names)
		return "", err
	}

	return merge(names, min(100, max(10, limit)))
}

// Reads and all lines from an input io.Reader and sorts them into a specified
// output file.  The sorting algorithm uses no more than limit number of lines
// at a time.
func sortLinesFile(r io.Reader, limit int, outfile string) error {
	name, err := sortLines(r, limit)
	if err != nil {
		return err
	}

	return os.Rename(name, outfile)
}

// Reads and all lines from an input io.Reader and writes them in sorted order
// into a specified output io.Writer.  The sorting algorithm uses no more than
// limit number of lines at a time.
func sortLinesWrite(r io.Reader, limit int, w io.Writer) error {
	name, err := sortLines(r, limit)
	if err != nil {
		return err
	}
	defer func() {
		deleteFile(name)
	}()

	f, err := os.Open(name)
	if err != nil {
		return err
	}
	defer func() {
		f.Close()
	}()

	in := bufio.NewReader(f)
	_, err = in.WriteTo(w)

	return err
}

func main() {
	var limit int
	var help bool

	flag.IntVar(&limit, "limit", 10000,
		"maximum number of lines per initial chunk")
	flag.BoolVar(&help, "help", false, "displays this help message")

	flag.Parse()

	// Guard against non-positive limit
	if limit < 1 {
		fmt.Fprintln(os.Stderr, "error: limit must be positive number")
		os.Exit(1)
	}

	if (flag.NArg() > 2) || help {
		fmt.Fprintln(os.Stderr,
			"Usage: filesort [-limit LIMIT] [INFILE [OUTFILE]]")
		flag.PrintDefaults()
		os.Exit(1)
	}

	var in io.Reader

	if flag.NArg() == 0 {
		in = os.Stdin
	} else {
		f, err := os.Open(flag.Arg(0))
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(2)
		}
		defer func() {
			f.Close()
		}()
		in = f
	}

	var err error

	if flag.NArg() == 2 {
		err = sortLinesFile(in, limit, flag.Arg(1))
	} else {
		err = sortLinesWrite(in, limit, os.Stdout)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(3)
	}
}
