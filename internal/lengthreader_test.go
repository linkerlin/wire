package internal_test

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"testing"

	"io"

	"github.com/influx6/faux/tests"
	"github.com/wirekit/wire/internal"
)

func TestLengthRecvReader_MultiRead(t *testing.T) {
	msg1 := buildMessage(10)
	reader := bytes.NewBuffer(makeMessage(string(msg1), 2))
	lr := internal.NewLengthRecvReader(reader, 2)

	_, err := lr.Read(nil)
	if err != internal.ErrReadStateError {
		tests.FailedWithError(err, "Should have failed to read first before calling ReadHeader")
	}
	tests.Passed("Should have failed to read first before calling ReadHeader")

	length, err := lr.ReadHeader()
	if err != nil {
		tests.FailedWithError(err, "Should have successfully read header from reader")
	}
	tests.Passed("Should have successfully read header from reader")

	half := length / 2
	incoming := make([]byte, half)
	n, err := lr.Read(incoming)
	if err != nil {
		tests.FailedWithError(err, "Should have successfully read data from reader")
	}
	tests.Passed("Should have successfully read data from reader")

	if n != half {
		tests.Info("Received: %d", n)
		tests.Info("Expected: %d", half)
		tests.Failed("Should have received a giving length of data")
	}
	tests.Passed("Should have received a giving length of data")

	if !bytes.Equal(msg1[:half], incoming) {
		tests.Info("Received: %#v", incoming)
		tests.Info("Expected: %#v", msg1[:half])
		tests.Failed("Should have successfully matched first message with expected")
	}
	tests.Passed("Should have successfully matched first message with expected")

	_, err = lr.ReadHeader()
	if err != internal.ErrPendingReads {
		tests.FailedWithError(err, "Should have failed to read next header will unfinished data")
	}
	tests.Passed("Should have failed to read next header will unfinished data")

	incoming2 := make([]byte, half)
	n, err = lr.Read(incoming2)
	if err != nil {
		tests.FailedWithError(err, "Should have successfully read data from reader")
	}
	tests.Passed("Should have successfully read data from reader")

	if n != half {
		tests.Info("Received: %d", n)
		tests.Info("Expected: %d", half)
		tests.Failed("Should have received a giving length of data")
	}
	tests.Passed("Should have received a giving length of data")

	if !bytes.Equal(msg1[half:], incoming2) {
		tests.Info("Received: %#v", incoming)
		tests.Info("Expected: %#v", msg1[half:])
		tests.Failed("Should have successfully matched second message with expected")
	}
	tests.Passed("Should have successfully matched second message with expected")

	_, err = lr.ReadHeader()
	if err != io.EOF {
		tests.FailedWithError(err, "Should have successfully reach end of reader")
	}
	tests.Passed("Should have successfully reach end of reader")
}

func TestLengthRecvReader_SingleRead(t *testing.T) {
	msg1 := buildMessage(10)
	reader := bytes.NewBuffer(makeMessage(string(msg1), 2))
	lr := internal.NewLengthRecvReader(reader, 2)

	_, err := lr.Read(nil)
	if err != internal.ErrReadStateError {
		tests.FailedWithError(err, "Should have failed to read first before calling ReadHeader")
	}
	tests.Passed("Should have failed to read first before calling ReadHeader")

	length, err := lr.ReadHeader()
	if err != nil {
		tests.FailedWithError(err, "Should have successfully read header from reader")
	}
	tests.Passed("Should have successfully read header from reader")

	incoming := make([]byte, length)
	n, err := lr.Read(incoming)
	if err != nil {
		tests.FailedWithError(err, "Should have successfully read data from reader")
	}
	tests.Passed("Should have successfully read data from reader")

	if n != length {
		tests.Info("Received: %d", n)
		tests.Info("Expected: %d", length)
		tests.Failed("Should have received a giving length of data")
	}
	tests.Passed("Should have received a giving length of data")

	if !bytes.Equal(msg1, incoming) {
		tests.Info("Received: %#v", incoming)
		tests.Info("Expected: %#v", msg1)
		tests.Failed("Should have successfully matched first message with expected")
	}
	tests.Passed("Should have successfully matched first message with expected")

	_, err = lr.ReadHeader()
	if err != io.EOF {
		tests.FailedWithError(err, "Should have successfully reach end of reader")
	}
	tests.Passed("Should have successfully reach end of reader")
}

func TestLengthReader_Header2(t *testing.T) {
	msg1 := buildMessage(10)
	msg2 := buildMessage(20)
	msg3 := buildMessage(30)
	reader := bytes.NewBuffer(makeMessages(2, msg1, msg2, msg3))
	lr := internal.NewLengthReader(reader, 2)

	rec1, err := lr.Read()
	if err != nil {
		tests.FailedWithError(err, "Should have successfully read first message")
	}
	tests.Passed("Should have successfully read first message")

	if !bytes.Equal(msg1, rec1) {
		tests.Info("Received: %#v", rec1)
		tests.Info("Expected: %#v", msg1)
		tests.Failed("Should have successfully matched first message with expected")
	}
	tests.Passed("Should have successfully matched first message with expected")

	rec2, err := lr.Read()
	if err != nil {
		tests.FailedWithError(err, "Should have successfully read second message")
	}
	tests.Passed("Should have successfully read second message")

	if !bytes.Equal(msg2, rec2) {
		tests.Info("Received: %#v", rec2)
		tests.Info("Expected: %#v", msg2)
		tests.Failed("Should have successfully matched second message with expected")
	}
	tests.Passed("Should have successfully matched second message with expected")

	rec3, err := lr.Read()
	if err != nil {
		tests.FailedWithError(err, "Should have successfully read third message")
	}
	tests.Passed("Should have successfully read third message")

	if !bytes.Equal(msg3, rec3) {
		tests.Info("Received: %#v", rec3)
		tests.Info("Expected: %#v", msg3)
		tests.Failed("Should have successfully matched second message with expected")
	}
	tests.Passed("Should have successfully matched second message with expected")

	if _, err = lr.Read(); err != io.EOF {
		tests.FailedWithError(err, "Should have received io.EOF error")
	}
	tests.Passed("Should have received io.EOF error")
}

func TestLengthReader_Header4(t *testing.T) {
	msg1 := buildMessage(15)
	msg2 := buildMessage(120)
	msg3 := buildMessage(40)
	reader := bytes.NewBuffer(makeMessages(4, msg1, msg2, msg3))
	lr := internal.NewLengthReader(reader, 4)

	rec1, err := lr.Read()
	if err != nil {
		tests.FailedWithError(err, "Should have successfully read first message")
	}
	tests.Passed("Should have successfully read first message")

	if !bytes.Equal(msg1, rec1) {
		tests.Info("Received: %#v", rec1)
		tests.Info("Expected: %#v", msg1)
		tests.Failed("Should have successfully matched first message with expected")
	}
	tests.Passed("Should have successfully matched first message with expected")

	rec2, err := lr.Read()
	if err != nil {
		tests.FailedWithError(err, "Should have successfully read second message")
	}
	tests.Passed("Should have successfully read second message")

	if !bytes.Equal(msg2, rec2) {
		tests.Info("Received: %#v", rec2)
		tests.Info("Expected: %#v", msg2)
		tests.Failed("Should have successfully matched second message with expected")
	}
	tests.Passed("Should have successfully matched second message with expected")

	rec3, err := lr.Read()
	if err != nil {
		tests.FailedWithError(err, "Should have successfully read third message")
	}
	tests.Passed("Should have successfully read third message")

	if !bytes.Equal(msg3, rec3) {
		tests.Info("Received: %#v", rec3)
		tests.Info("Expected: %#v", msg3)
		tests.Failed("Should have successfully matched second message with expected")
	}
	tests.Passed("Should have successfully matched second message with expected")

	if _, err = lr.Read(); err != io.EOF {
		tests.FailedWithError(err, "Should have received io.EOF error")
	}
	tests.Passed("Should have received io.EOF error")
}

func TestLengthReader_Header8(t *testing.T) {
	msg1 := buildMessage(15)
	msg2 := buildMessage(120)
	msg3 := buildMessage(40)
	reader := bytes.NewBuffer(makeMessages(8, msg1, msg2, msg3))
	lr := internal.NewLengthReader(reader, 8)

	rec1, err := lr.Read()
	if err != nil {
		tests.FailedWithError(err, "Should have successfully read first message")
	}
	tests.Passed("Should have successfully read first message")

	if !bytes.Equal(msg1, rec1) {
		tests.Info("Received: %#v", rec1)
		tests.Info("Expected: %#v", msg1)
		tests.Failed("Should have successfully matched first message with expected")
	}
	tests.Passed("Should have successfully matched first message with expected")

	rec2, err := lr.Read()
	if err != nil {
		tests.FailedWithError(err, "Should have successfully read second message")
	}
	tests.Passed("Should have successfully read second message")

	if !bytes.Equal(msg2, rec2) {
		tests.Info("Received: %#v", rec2)
		tests.Info("Expected: %#v", msg2)
		tests.Failed("Should have successfully matched second message with expected")
	}
	tests.Passed("Should have successfully matched second message with expected")

	rec3, err := lr.Read()
	if err != nil {
		tests.FailedWithError(err, "Should have successfully read third message")
	}
	tests.Passed("Should have successfully read third message")

	if !bytes.Equal(msg3, rec3) {
		tests.Info("Received: %#v", rec3)
		tests.Info("Expected: %#v", msg3)
		tests.Failed("Should have successfully matched second message with expected")
	}
	tests.Passed("Should have successfully matched second message with expected")

	if _, err = lr.Read(); err != io.EOF {
		tests.FailedWithError(err, "Should have received io.EOF error")
	}
	tests.Passed("Should have received io.EOF error")
}

func TestLengthReader_HeaderUncompletedData(t *testing.T) {
	msg1 := buildMessage(256)
	sizedMsg := makeMessage(string(msg1), 4)

	reader := bytes.NewBuffer(sizedMsg[:80])
	lr := internal.NewLengthReader(reader, 4)

	_, err := lr.Read()
	if err != internal.ErrUncompletedTransfer {
		tests.Failed("Should have received ErrUncompletedTransfer from reader")
	}
	tests.PassedWithError(err, "Should have received ErrUncompletedTransfer from reader")
}

func TestLengthReader_InvalidHeader(t *testing.T) {
	msg1 := buildMessage(256)
	sizedMsg := makeMessage(string(msg1), 2)

	reader := bytes.NewBuffer(sizedMsg[:80])
	lr := internal.NewLengthReader(reader, 4)

	_, err := lr.Read()
	if err != internal.ErrUncompletedTransfer {
		tests.FailedWithError(err, "Should have received ErrUncompletedTransfer from reader")
	}
	tests.PassedWithError(err, "Should have received ErrUncompletedTransfer from reader")
}

//**********************************************************************************
// utilities
//**********************************************************************************

func buildMessage(size int) []byte {
	data := make([]byte, size)
	rand.Read(data)
	return data
}

func makeMessages(size int, msgs ...[]byte) []byte {
	var m []byte
	for _, msg := range msgs {
		m = append(m, makeMessage(string(msg), size)...)
	}
	return m
}

func makeMessage(msg string, size int) []byte {
	header := make([]byte, size)
	switch size {
	case 2:
		binary.BigEndian.PutUint16(header, uint16(len(msg)))
	case 4:
		binary.BigEndian.PutUint32(header, uint32(len(msg)))
	case 8:
		binary.BigEndian.PutUint64(header, uint64(len(msg)))
	}

	return append(header, []byte(msg)...)
}
