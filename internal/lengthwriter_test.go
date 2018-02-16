package internal_test

import (
	"bytes"
	"testing"

	"github.com/influx6/faux/tests"
	"github.com/influx6/mnet/internal"
)

func TestActionLengthWriter_Size2(t *testing.T) {
	var bu bytes.Buffer
	lw := internal.NewActionLengthWriter(func(size []byte, data []byte) error {
		if _, err := bu.Write(size); err != nil {
			return err
		}

		if _, err := bu.Write(data); err != nil {
			return err
		}

		return nil
	}, 2, 10)

	data := buildMessage(10)
	total, err := lw.Write(data)
	if err != nil {
		tests.FailedWithError(err, "Should have successfully written data to writer")
	}
	tests.Passed("Should have successfully written data to writer")

	if total != 10 {
		tests.Info("Expected: %d", 10)
		tests.Info("Received: %d", total)
		tests.Failed("Should have written given size of data to writer")
	}
	tests.Passed("Should have written given size of data to writer")

	if err := lw.Close(); err != nil {
		tests.FailedWithError(err, "Should have successfully closed writer")
	}
	tests.Passed("Should have successfully closed writer")

	lr := internal.NewLengthReader(&bu, 2)
	rec, err := lr.Read()
	if err != nil {
		tests.FailedWithError(err, "Should have successfully read written data from reader")
	}
	tests.Passed("Should have successfully read written data from reader")

	if !bytes.Equal(data, rec) {
		tests.Info("Received: %#v", rec)
		tests.Info("Expected: %#v", data)
		tests.Failed("Should have matched written data with read data")
	}
	tests.Passed("Should have matched written data with read data")
}

func TestLengthWriter_Size2(t *testing.T) {
	var bu bytes.Buffer
	lw := internal.NewLengthWriter(&bu, 2, 10)

	data := buildMessage(10)
	total, err := lw.Write(data)
	if err != nil {
		tests.FailedWithError(err, "Should have successfully written data to writer")
	}
	tests.Passed("Should have successfully written data to writer")

	if total != 10 {
		tests.Info("Expected: %d", 10)
		tests.Info("Received: %d", total)
		tests.Failed("Should have written given size of data to writer")
	}
	tests.Passed("Should have written given size of data to writer")

	if err := lw.Close(); err != nil {
		tests.FailedWithError(err, "Should have successfully closed writer")
	}
	tests.Passed("Should have successfully closed writer")

	lr := internal.NewLengthReader(&bu, 2)
	rec, err := lr.Read()
	if err != nil {
		tests.FailedWithError(err, "Should have successfully read written data from reader")
	}
	tests.Passed("Should have successfully read written data from reader")

	if !bytes.Equal(data, rec) {
		tests.Info("Received: %#v", rec)
		tests.Info("Expected: %#v", data)
		tests.Failed("Should have matched written data with read data")
	}
	tests.Passed("Should have matched written data with read data")
}

func TestLengthWriter_Size4(t *testing.T) {
	var bu bytes.Buffer
	lw := internal.NewLengthWriter(&bu, 4, 10)

	data := buildMessage(10)
	total, err := lw.Write(data)
	if err != nil {
		tests.FailedWithError(err, "Should have successfully written data to writer")
	}
	tests.Passed("Should have successfully written data to writer")

	if total != 10 {
		tests.Info("Expected: %d", 10)
		tests.Info("Received: %d", total)
		tests.Failed("Should have written given size of data to writer")
	}
	tests.Passed("Should have written given size of data to writer")

	if err := lw.Close(); err != nil {
		tests.FailedWithError(err, "Should have successfully closed writer")
	}
	tests.Passed("Should have successfully closed writer")

	lr := internal.NewLengthReader(&bu, 4)
	rec, err := lr.Read()
	if err != nil {
		tests.FailedWithError(err, "Should have successfully read written data from reader")
	}
	tests.Passed("Should have successfully read written data from reader")

	if !bytes.Equal(data, rec) {
		tests.Info("Received: %#v", rec)
		tests.Info("Expected: %#v", data)
		tests.Failed("Should have matched written data with read data")
	}
	tests.Passed("Should have matched written data with read data")
}

func TestLengthWriter_Size8(t *testing.T) {
	var bu bytes.Buffer
	lw := internal.NewLengthWriter(&bu, 8, 10)

	data := buildMessage(10)
	total, err := lw.Write(data)
	if err != nil {
		tests.FailedWithError(err, "Should have successfully written data to writer")
	}
	tests.Passed("Should have successfully written data to writer")

	if total != 10 {
		tests.Info("Expected: %d", 10)
		tests.Info("Received: %d", total)
		tests.Failed("Should have written given size of data to writer")
	}
	tests.Passed("Should have written given size of data to writer")

	if err := lw.Close(); err != nil {
		tests.FailedWithError(err, "Should have successfully closed writer")
	}
	tests.Passed("Should have successfully closed writer")

	lr := internal.NewLengthReader(&bu, 8)
	rec, err := lr.Read()
	if err != nil {
		tests.FailedWithError(err, "Should have successfully read written data from reader")
	}
	tests.Passed("Should have successfully read written data from reader")

	if !bytes.Equal(data, rec) {
		tests.Info("Received: %#v", rec)
		tests.Info("Expected: %#v", data)
		tests.Failed("Should have matched written data with read data")
	}
	tests.Passed("Should have matched written data with read data")
}
