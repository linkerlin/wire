package tlsfs_test

import (
	"bytes"
	"testing"

	"github.com/influx6/faux/tests"
	"github.com/wirekit/wire/tlsfs"
)

func TestZapFile_Gzip(t *testing.T) {
	var track tlsfs.ZapTrack
	track.Name = "warzone.txt"
	track.Data = []byte("centries of years ago, a war began.")
	
	var zfile tlsfs.ZapFile
	zfile.Name = "warzone-drs"
	zfile.Tracks = append(zfile.Tracks, track)
	
	var zapData bytes.Buffer
	if _, err := zfile.WriteGzippedTo(&zapData); err != nil {
		tests.FailedWithError(err, "Should have successffully written zap file contents")
	}
	tests.Passed("Should have successffully written zap file contents")
	
	var zfile2 tlsfs.ZapFile
	if err := zfile2.UnmarshalReader(&zapData); err != nil {
		tests.FailedWithError(err, "Should have successfully demarshalled data of zapfile")
	}
	tests.Passed("Should have successfully demarshalled data of zapfile")
	
	if zfile2.Name != zfile.Name {
		tests.Info("Received: %q", zfile2.Name)
		tests.Info("Expected: %q", zfile.Name)
		tests.Failed("Should have matched name source and demarshalled ZFile")
	}
	tests.Passed("Should have matched name source and demarshalled ZFile")
	
	if len(zfile2.Tracks) != len(zfile.Tracks) {
		tests.Info("Received: %d", len(zfile2.Tracks))
		tests.Info("Expected: %d", len(zfile.Tracks))
		tests.Failed("Should have matched length of tracks in source and demarshaled")
	}
	tests.Passed("Should have matched length of tracks in source and demarshaled")
	
	for index, track := range zfile2.Tracks {
		xtrack := zfile.Tracks[index]
		
		if xtrack.Name != track.Name {
			tests.Info("Received: %q", track.Name)
			tests.Info("Expected: %q", xtrack.Name)
			tests.Failed("tlsfs.ZapTrack at index %d in %+q tlsfs.ZapFile does not match in name")
		}
		
		if !bytes.Equal(xtrack.Data, track.Data) {
			tests.Info("Received: %+q", track.Data)
			tests.Info("Expected: %+q", xtrack.Data)
			tests.Failed("tlsfs.ZapTrack at index %d in %+q tlsfs.ZapFile does not match data")
		}
	}
}

func TestZapFile_Flat(t *testing.T) {
	var track tlsfs.ZapTrack
	track.Name = "warzone.txt"
	track.Data = []byte("centries of years ago, a war began.")

	var zfile tlsfs.ZapFile
	zfile.Name = "warzone-drs"
	zfile.Tracks = append(zfile.Tracks, track)

	var zapData bytes.Buffer
	if _, err := zfile.WriteFlatTo(&zapData); err != nil {
		tests.FailedWithError(err, "Should have successffully written zap file contents")
	}
	tests.Passed("Should have successffully written zap file contents")

	var zfile2 tlsfs.ZapFile
	if err := zfile2.UnmarshalReader(&zapData); err != nil {
		tests.FailedWithError(err, "Should have successfully demarshalled data of zapfile")
	}
	tests.Passed("Should have successfully demarshalled data of zapfile")

	if zfile2.Name != zfile.Name {
		tests.Info("Received: %q", zfile2.Name)
		tests.Info("Expected: %q", zfile.Name)
		tests.Failed("Should have matched name source and demarshalled ZFile")
	}
	tests.Passed("Should have matched name source and demarshalled ZFile")

	if len(zfile2.Tracks) != len(zfile.Tracks) {
		tests.Info("Received: %d", len(zfile2.Tracks))
		tests.Info("Expected: %d", len(zfile.Tracks))
		tests.Failed("Should have matched length of tracks in source and demarshaled")
	}
	tests.Passed("Should have matched length of tracks in source and demarshaled")

	for index, track := range zfile2.Tracks {
		xtrack := zfile.Tracks[index]

		if xtrack.Name != track.Name {
			tests.Info("Received: %q", track.Name)
			tests.Info("Expected: %q", xtrack.Name)
			tests.Failed("tlsfs.ZapTrack at index %d in %+q tlsfs.ZapFile does not match in name")
		}

		if !bytes.Equal(xtrack.Data, track.Data) {
			tests.Info("Received: %+q", track.Data)
			tests.Info("Expected: %+q", xtrack.Data)
			tests.Failed("tlsfs.ZapTrack at index %d in %+q tlsfs.ZapFile does not match data")
		}
	}
}

func TestZapTrack(t *testing.T) {
	var track tlsfs.ZapTrack
	track.Name = "warzone.txt"
	track.Data = []byte("centries of years ago, a war began.")

	expectedSize := int64(len(track.Name) + len(track.Data) + 12)

	var trackData bytes.Buffer
	written, err := track.WriteTo(&trackData)
	if err != nil {
		tests.FailedWithError(err, "Should have sucessfully written to writer")
	}
	tests.Passed("Should have sucessfully written to writer")

	if written != expectedSize {
		tests.Info("Received: %d", written)
		tests.Info("Expected: %d", expectedSize)
		tests.Failed("Should have written the of giving size to writer")
	}
	tests.Passed("Should have written the of giving size to writer")

	var newTrack tlsfs.ZapTrack
	if err := newTrack.UnmarshalBytes(trackData.Bytes()); err != nil {
		tests.FailedWithError(err, "Should have successfully unmarshalled data back into tlsfs.ZapTrack")
	}
	tests.Passed("Should have successfully unmarshalled data back into tlsfs.ZapTrack")

	if newTrack.Name != track.Name {
		tests.Info("Received: %q", newTrack.Name)
		tests.Info("Expected: %q", track.Name)
		tests.Failed("Should have matched name of old track to demarshalled track")
	}
	tests.Passed("Should have matched name of old track to demarshalled track")

	if !bytes.Equal(track.Data, newTrack.Data) {
		tests.Info("Received: %+q", newTrack.Data)
		tests.Info("Expected: %+q", track.Data)
		tests.Failed("Should have matched data of old track to demarshalled track")
	}
	tests.Passed("Should have matched data of old track to demarshalled track")
}
