package hostsetup

import "testing"

func TestHeadWriterKeepsBoundedPrefix(t *testing.T) {
	writer := &headWriter{limit: 5}
	for _, chunk := range []string{"abc", "defgh", "ij"} {
		if count, err := writer.Write([]byte(chunk)); err != nil || count != len(chunk) {
			t.Fatalf("write %q = %d, %v", chunk, count, err)
		}
	}
	if writer.String() != "abcde" {
		t.Fatalf("kept %q", writer.String())
	}
}
