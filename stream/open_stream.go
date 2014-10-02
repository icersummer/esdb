package stream

import (
	"bytes"
	"errors"
	"io"
	"os"
	"sort"

	"github.com/customerio/esdb/binary"
	"github.com/customerio/esdb/sst"
)

var CORRUPTED_HEADER = errors.New("Incorrect stream file header.")

type openStream struct {
	stream io.ReadWriteSeeker
	tails  map[string]int64
	closed bool
	offset int64
	length int
}

// Creates a new open stream at the given path. If the
// file already exists, an error will be returned.
func New(path string) (Stream, error) {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0755)
	if err != nil {
		return nil, err
	}

	return createOpenStream(file)
}

func read(path string) (Stream, error) {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0755)
	if err != nil {
		return nil, err
	}

	return newOpenStream(file)
}

func createOpenStream(stream io.ReadWriteSeeker) (Stream, error) {
	_, err := stream.Seek(0, 0)
	if err != nil {
		return nil, err
	}

	offset, err := stream.Write([]byte(MAGIC_HEADER))
	if err != nil {
		return nil, err
	}

	return &openStream{
		stream: stream,
		tails:  make(map[string]int64),
		offset: int64(offset),
	}, nil
}

func newOpenStream(stream io.ReadWriteSeeker) (Stream, error) {
	s := &openStream{stream: stream}

	_, err := stream.Seek(0, 0)
	if err != nil {
		return nil, err
	}

	tails, offset, length, err := scan(s.stream)

	s.tails = tails
	s.offset = offset
	s.length = length

	return s, err
}

func (s *openStream) Write(data []byte, indexes []string) (int, error) {
	if s.Closed() {
		return 0, WRITING_TO_CLOSED_STREAM
	}

	_, err := s.stream.Seek(s.offset, 0)
	if err != nil {
		return 0, err
	}

	offsets := make(map[string]int64)

	for _, index := range indexes {
		if off, ok := s.tails[index]; ok {
			offsets[index] = off
		} else {
			offsets[index] = 0
		}
	}

	event := newEvent(data, offsets)

	buf := bytes.NewBuffer([]byte{})

	_, err = event.push(buf)
	if err != nil {
		return 0, err
	}

	written, err := s.stream.Write(buf.Bytes())
	if err != nil {
		return 0, err
	}

	for _, index := range indexes {
		s.tails[index] = s.offset
	}

	s.offset += int64(written)
	s.length += 1

	return written, nil
}

func (s *openStream) ScanIndex(index string, scanner Scanner) error {
	off := s.tails[index]

	for off > 0 {
		s.stream.Seek(off, 0)

		if event, err := pullEvent(s.stream); err == nil {
			scanner(event)
			off = event.offsets[index]
		} else {
			return err
		}
	}

	return nil
}

func (s *openStream) Iterate(scanner Scanner) error {
	s.stream.Seek(int64(len(MAGIC_HEADER)), 0)

	var event *Event
	var err error

	for err == nil {
		if event, err = pullEvent(s.stream); err == nil {
			scanner(event)
		}
	}

	if err == io.EOF {
		return nil
	} else {
		return err
	}
}

func (s *openStream) Closed() bool {
	return s.closed
}

func (s *openStream) Close() (err error) {
	if s.Closed() {
		return
	}

	// Write nil event, to signal end of events.
	binary.WriteInt32(s.stream, 0)

	indexes := make(sort.StringSlice, 0, len(s.tails))

	for name, _ := range s.tails {
		indexes = append(indexes, name)
	}

	sort.Stable(indexes)

	buf := new(bytes.Buffer)
	st := sst.NewWriter(buf)

	// For each grouping or index, we index the section's
	// byte offset in the file and the length in bytes
	// of all data in the grouping/index.
	for _, name := range indexes {
		buf := new(bytes.Buffer)

		binary.WriteUvarint64(buf, s.tails[name])

		if err = st.Set([]byte(name), buf.Bytes()); err != nil {
			return
		}
	}

	if err = st.Close(); err != nil {
		return
	}

	binary.WriteInt64(buf, int64(len(buf.Bytes())))
	buf.Write([]byte(MAGIC_FOOTER))

	_, err = buf.WriteTo(s.stream)
	if err == nil {
		s.closed = true
	}

	return
}

func scan(stream io.Reader) (tails map[string]int64, offset int64, length int, err error) {
	tails = make(map[string]int64)

	var event *Event

	header := binary.ReadBytes(stream, int64(len(MAGIC_HEADER)))

	if string(header) != string(MAGIC_HEADER) {
		err = CORRUPTED_HEADER
		return
	}

	offset += int64(len(header))

	for event, err = pullEvent(stream); err == nil; event, err = pullEvent(stream) {
		for index, _ := range event.offsets {
			tails[index] = offset
		}

		// set tail for all event indexes
		offset += int64(event.length())
		length += 1
	}

	// If we reached the end of the file, or we
	// couldn't decode the event, stop populating.
	if err == io.EOF || err == CORRUPTED_EVENT {
		err = nil
	}

	return
}
