package tlsfs

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"sync/atomic"
	"time"

	"encoding/binary"

	"compress/gzip"

	"github.com/influx6/faux/pools/pbytes"
	"github.com/wirekit/wire/certificates"
	"github.com/wirekit/wire/internal"
)

var (
	bits = pbytes.NewBytesPool(128, 30)

	// ErrMismatchedDecompressSize is return when giving size of decompressed is
	// not the size expected of ZapTrack.
	ErrMismatchedDecompressSize = errors.New("tlsfs.ZapTrack decompress data mismatched with expected size")

	// ErrInvalidZapTrackBytes is returned when byte slices differes from zaptrack layout.
	ErrInvalidZapTrackBytes = errors.New("[]byte content is not a valid ZapTrack data")

	// ErrInvalidRead is returned when expected read size is not met by readers .Read() call.
	ErrInvalidRead = errors.New("read failed to match expected header size")
)

//*************************************************************
// ZapFS: Tracks and Files
//*************************************************************

// ZapFile defines a internal file format that stores all
// internal tracks as a single, gzipped compressed file format.
type ZapFile struct {
	Name   string
	Tracks []ZapTrack
}

// WriteFlatTo writes the data of a ZapFile without any compression
// into provided writer.
func (zt ZapFile) WriteFlatTo(w io.Writer) (int64, error) {
	return zt.format(false, w)
}

// WriteGzippedTo writes the data of a ZapFile with gzip compression
// into provided writer.
func (zt ZapFile) WriteGzippedTo(w io.Writer) (int64, error) {
	return zt.format(true, w)
}

// UnmarshalReader takes a giving reader and attempts to decode it's
// content has a ZapFile either compressed or uncompressed.
func (zt *ZapFile) UnmarshalReader(r io.Reader) error {
	br := bufio.NewReader(r)
	header := make([]byte, 9)

	hread, err := br.Read(header)
	if err != nil {
		return err
	}

	if hread != 9 {
		return errors.New("failed to read length header")
	}

	cbit := int8(header[0])
	tagLen := int(binary.BigEndian.Uint32(header[1:5]))
	contentLen := int(binary.BigEndian.Uint32(header[5:9]))

	tag := make([]byte, tagLen)
	tread, err := br.Read(tag)
	if err != nil {
		return err
	}

	if tread != tagLen {
		return errors.New("failed to read tag name data")
	}

	zt.Name = string(tag)

	var lr *internal.LengthRecvReader
	if cbit == 0 {
		lr = internal.NewLengthRecvReader(br, 4)
	} else {
		gr, err := gzip.NewReader(br)
		if err != nil {
			return err
		}

		defer gr.Close()
		lr = internal.NewLengthRecvReader(gr, 4)
	}

	var tracks []ZapTrack
	var trackData []byte

	var readin int

	for {
		header, err := lr.ReadHeader()
		if err != nil {
			if err != io.EOF {
				return err
			}

			break
		}

		// make space for data to be read, then put back header of
		// total data length.
		trackData = make([]byte, header+4)
		binary.BigEndian.PutUint32(trackData[0:4], uint32(header))

		// Read into space for track data after length area of data.
		n, err := lr.Read(trackData[4:])
		if err != nil {
			return err
		}

		if n != header {
			return ErrInvalidRead
		}

		readin += header + 4

		var track ZapTrack
		if err := track.UnmarshalBytes(trackData); err != nil {
			return err
		}

		tracks = append(tracks, track)
	}

	if contentLen != readin {
		return errors.New("invalid content length with content read")
	}

	zt.Tracks = tracks
	return nil
}

func (zt ZapFile) format(gzipped bool, w io.Writer) (int64, error) {
	tagLen := len(zt.Name)

	hzone := tagLen + 9
	header := make([]byte, hzone)

	if gzipped {
		header[0] = byte(1)
	} else {
		header[0] = byte(0)
	}

	// Write into header total size of name.
	binary.BigEndian.PutUint32(header[1:5], uint32(tagLen))

	var contentLen int
	var contents bytes.Buffer
	if gzipped {
		gzw := gzip.NewWriter(&contents)
		gzw.Name = zt.Name
		gzw.ModTime = time.Now()

		wc := &counterWriter{w: gzw}
		for _, track := range zt.Tracks {
			if _, err := track.WriteTo(wc); err != nil {
				return 0, err
			}
		}

		if err := gzw.Flush(); err != nil {
			return 0, err
		}

		if err := gzw.Close(); err != nil {
			return 0, err
		}

		contentLen = int(atomic.LoadInt64(&wc.n))
	} else {
		for _, track := range zt.Tracks {
			if _, err := track.WriteTo(&contents); err != nil {
				return 0, err
			}
		}

		contentLen = contents.Len()
	}

	// Write into header total size of content.
	binary.BigEndian.PutUint32(header[5:9], uint32(contentLen))

	// copy into available space name of content and ensure length range.
	if copy(header[9:], []byte(zt.Name)) != tagLen {
		return 0, io.ErrShortWrite
	}

	nheader, err := w.Write(header)
	if err != nil {
		return 0, err
	}

	cheader, err := io.Copy(w, &contents)
	if err != nil {
		return 0, err
	}

	return cheader + int64(nheader), nil
}

func (zt ZapFile) trackSize() int {
	var total int
	for _, track := range zt.Tracks {
		total += len(track.Name) + len(track.Data) + 12
	}
	return total
}

// ZapTrack defines a structure which defines a giving
// data track of a continuous-single-lined file data track.
// It represent a single data entity which is represented
// as a single file by the ZapFile format.
type ZapTrack struct {
	Name string
	Data []byte
}

// UnmarshalBytes takes giving bytes validating its's
// content to match the ZapTrack layout format, then
// setting it's Fields to appropriate contents of the
// data.
func (zt *ZapTrack) UnmarshalBytes(b []byte) error {
	bLen := len(b)
	if bLen <= 12 {
		return ErrInvalidZapTrackBytes
	}

	data := b[12:]
	zapsize := binary.BigEndian.Uint32(b[0:4])
	if bLen-4 != int(zapsize) {
		return ErrInvalidZapTrackBytes
	}

	tagsize := binary.BigEndian.Uint32(b[4:8])
	if len(data) <= int(tagsize) {
		return ErrInvalidZapTrackBytes
	}

	tagName := data[0:int(tagsize)]
	data = data[int(tagsize):]

	datasize := binary.BigEndian.Uint32(b[8:12])
	if len(data) != int(datasize) {
		return ErrInvalidZapTrackBytes
	}

	zt.Name = string(tagName)
	zt.Data = data
	return nil
}

// WriteTo implements the io.WriterTo taht writes the
// contents of a ZapTrack as a uncompressed data stream
// with appropriate header information regarding the
// data.
func (zt ZapTrack) WriteTo(w io.Writer) (int64, error) {
	dmsize := make([]byte, 12)
	bitsize := len(zt.Name) + len(zt.Data) + 8
	binary.BigEndian.PutUint32(dmsize[0:4], uint32(bitsize))
	binary.BigEndian.PutUint32(dmsize[4:8], uint32(len(zt.Name)))
	binary.BigEndian.PutUint32(dmsize[8:12], uint32(len(zt.Data)))

	decompress := bits.Get(int(bitsize))
	defer bits.Put(decompress)

	if n, err := decompress.Write(dmsize); err != nil {
		return int64(n), err
	}

	if n, err := decompress.Write([]byte(zt.Name)); err != nil {
		return int64(n), err
	}

	if n, err := decompress.Write(zt.Data); err != nil {
		return int64(n), err
	}

	nx, err := decompress.WriteTo(w)
	if err != nil {
		return nx, err
	}

	if int(nx)-4 != bitsize {
		return nx, io.ErrShortWrite
	}

	return nx, nil
}

//*************************************************************
// ZapFS and TLSFS interface and implementation
//*************************************************************

// ZapFS defines an interface that exposes a filesystem to
// power the storage/retrieval of tls certificates with ease.
type ZapFS interface {
	Read(string) (ZapFile, error)
	Write(string, ...ZapTrack) error
}

func TLSFS(authority certificates.CertificateAuthorityProfile) {

}

//*************************************************************
//  internal types and methods
//*************************************************************

type counterWriter struct {
	n int64
	w io.Writer
}

func (c *counterWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	atomic.AddInt64(&c.n, int64(n))
	return n, err
}
