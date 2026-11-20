package itop

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// StreamBlob fetches one object's blob attribute and writes the decoded bytes
// to dst while the response is still being read.
func (c *Client) StreamBlob(ctx context.Context, class, id, field string, dst io.Writer) (BlobMeta, error) {
	resp, cancel, err := c.do(ctx, "core/get", map[string]any{
		"class":         class,
		"key":           keyValue(id),
		"output_fields": field,
	})
	if err != nil {
		return BlobMeta{}, err
	}
	defer cancel()
	defer resp.Body.Close()

	dec := json.NewDecoder(&capReader{r: resp.Body, n: maxResponseBytes})
	cw := &countWriter{w: dst}
	var (
		meta    BlobMeta
		code    int
		message string
		found   bool
	)
	werr := walkObject(dec, func(key string) error {
		switch key {
		case "code":
			return dec.Decode(&code)
		case "message":
			return dec.Decode(&message)
		case "objects":
			return walkObject(dec, func(string) error {
				return walkObject(dec, func(k string) error {
					if k != "fields" {
						return skip(dec)
					}
					return walkObject(dec, func(f string) error {
						if f != field {
							return skip(dec)
						}
						found = true
						return walkObject(dec, func(bk string) error {
							return blobKey(dec, bk, &meta, cw)
						})
					})
				})
			})
		}
		return skip(dec)
	})
	if werr != nil {
		return BlobMeta{}, c.readError(werr)
	}
	if code != 0 {
		return BlobMeta{}, c.classify(code, message)
	}
	if !found {
		return BlobMeta{}, c.fail(ErrAPI, "no %s value returned for %s %s", field, class, id)
	}
	meta.Size = cw.n
	return meta, nil
}

func blobKey(dec *json.Decoder, key string, meta *BlobMeta, dst io.Writer) error {
	switch key {
	case "data":
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		s, ok := tok.(string)
		if !ok {
			return errors.New("blob data is not a string")
		}
		_, err = io.Copy(dst, base64.NewDecoder(base64.StdEncoding, strings.NewReader(s)))
		return err
	case "mimetype":
		return dec.Decode(&meta.MimeType)
	case "filename":
		return dec.Decode(&meta.Filename)
	}
	return skip(dec)
}

// walkObject consumes one JSON object, calling each with every key; each must
// consume the key's value. A JSON null is accepted as an empty object.
func walkObject(dec *json.Decoder, each func(key string) error) error {
	ok, err := openObject(dec)
	if err != nil || !ok {
		return err
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		key, _ := tok.(string)
		if err := each(key); err != nil {
			return err
		}
	}
	_, err = dec.Token()
	return err
}

// openObject consumes the opening brace; false means the value was null.
func openObject(dec *json.Decoder) (bool, error) {
	tok, err := dec.Token()
	if err != nil {
		return false, err
	}
	if tok == nil {
		return false, nil
	}
	if d, ok := tok.(json.Delim); ok && d == '{' {
		return true, nil
	}
	return false, errors.New("expected a JSON object")
}

func skip(dec *json.Decoder) error {
	var raw json.RawMessage
	return dec.Decode(&raw)
}
