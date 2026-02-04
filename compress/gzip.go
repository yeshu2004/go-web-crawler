package compress

import (
	"bytes"
	"compress/gzip"
	"io"
)


func GzipCompress(data []byte)([]byte, error){
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf);

	if _, err := zw.Write(data); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func GzipDecompress(data []byte)([]byte, error){
	var buf bytes.Buffer
	zr, err := gzip.NewReader(&buf);
	if err != nil{
		return nil, err
	}
	defer zr.Close()

	return io.ReadAll(&buf);
}