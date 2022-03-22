package av

import "io"

type AVFrameReader interface {
	ReadAVFrame(*AVFrame) error
}

type AVFrameWriter interface {
	WriteAVFrame(*AVFrame) error
}

type AVFrameReadCloser interface {
	AVFrameReader
	io.Closer
}

type AVFrameWriteCloser interface {
	AVFrameWriter
	io.Closer
}

type AVFrameReadWriter interface {
	AVFrameReader
	AVFrameWriter
}

type AVFrameReadWriteCloser interface {
	AVFrameReadWriter
	io.Closer
}

type AVPacketReader interface {
	ReadAVPacket(*AVPacket) error
}

type AVPacketWriter interface {
	WriteAVPacket(*AVPacket) error
}

type AVPacketReadCloser interface {
	AVPacketReader
	io.Closer
}

type AVPacketWriteCloser interface {
	AVPacketWriter
	io.Closer
}

type AVPacketReadWriter interface {
	AVPacketReader
	AVPacketWriter
}

type AVPacketReadWriteCloser interface {
	AVPacketReadWriter
	io.Closer
}