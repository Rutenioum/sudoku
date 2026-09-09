package sudoku

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"testing"
)

// These digests pin the pre-optimization wire stream, including padding,
// protected prefixes and residual-bit markers, with a fixed random seed.
func TestCodecWireCompatibility(t *testing.T) {
	goldens := map[string]string{
		"prefer_ascii/packed=false/padding=0":     "3d71c28ecd52a4bc85a96d1b33338e2fb7ef232ccdd2d637f20f265c5d1ce86b",
		"prefer_ascii/packed=false/padding=10":    "fd8447e168016cec7d2b6f8f9efe5b0de4e0f7192810ca0006f8cca4dcfae4e6",
		"prefer_ascii/packed=false/padding=100":   "d15ace0e54d1637beb45cdf588461e683cd9f83ca1867c1d6b95caa8ecf6b252",
		"prefer_ascii/packed=true/padding=0":      "da4b1867414d76bd02359cead3207929c883128dc4006866c5c89bab2c0c6d06",
		"prefer_ascii/packed=true/padding=10":     "3e7e10f299b91afe0af5016e7d205d9a7c9a45b14a23190bbe8161640eb29487",
		"prefer_ascii/packed=true/padding=100":    "a9a62c246cc4d1ce84818697adf479f302760e95ccc2f809ab181beefa64148e",
		"prefer_entropy/packed=false/padding=0":   "680b1507632a5aeba3c4b0ad438d0c67abf02e34a0e866c1b73f9f8ab8b606b5",
		"prefer_entropy/packed=false/padding=10":  "51676390cc94a8aad08701639e397f47f425b98f1833da0880c12525c3fe1677",
		"prefer_entropy/packed=false/padding=100": "5c7d687c6b5785b83024f1d64a53c6c3f0cf3cf4d9f17de511072196dd73df61",
		"prefer_entropy/packed=true/padding=0":    "d88a51e8bb5c7bbeac2bd1d4c2698078cf035001e63d209759708df6a3ba5d43",
		"prefer_entropy/packed=true/padding=10":   "d177e19abaa3ae6d0573ce9aca4385e731c0ba2eb751fce51f7f4ae9e6ee9ce6",
		"prefer_entropy/packed=true/padding=100":  "d7028cf1f39f15cfa90b48351a72ace24d4d173e3f0b04964203ea16f71e7cfa",
		"custom/packed=false/padding=0":           "1ed09fecfd26e440ded7b03b957d9956f4bbb9ab12ee628a639ff225b9994431",
		"custom/packed=false/padding=10":          "907c9bcf2294685ac92553476a93226903b92bba4dbfc71ab849ad031ae36148",
		"custom/packed=false/padding=100":         "bf3e6d5c9cb1eaf46312e9438822981169ac1be9d7efec313616185f79043d9e",
		"custom/packed=true/padding=0":            "86973270ed688d486b30468a1d0be4aec004d0e9200bf1dc564add61329f3eb2",
		"custom/packed=true/padding=10":           "d3ff98a0f568607c7799532cc6bed740472bc06e683710c3570a430e722ce6e5",
		"custom/packed=true/padding=100":          "7152d54386c1725b4e07180279ba520e7b6736fef7096afa8f0132b73c3416f0",
	}

	for _, mode := range []string{"prefer_ascii", "prefer_entropy", "custom"} {
		preference, pattern := mode, ""
		if mode == "custom" {
			preference, pattern = "prefer_entropy", "xvpvvpxv"
		}
		table, err := NewTableWithCustom("wire-compatibility", preference, pattern)
		if err != nil {
			t.Fatal(err)
		}
		for _, packed := range []bool{false, true} {
			for _, padding := range []int{0, 10, 100} {
				name := fmt.Sprintf("%s/packed=%v/padding=%d", mode, packed, padding)
				t.Run(name, func(t *testing.T) {
					var wire bytes.Buffer
					var writer net.Conn
					if packed {
						c := NewPackedConn(writeOnlyConn{Writer: &wire}, table, padding, padding)
						c.rng = newSudokuRand(123456)
						writer = c
					} else {
						c := NewConn(writeOnlyConn{Writer: &wire}, table, padding, padding, false)
						c.rng = newSudokuRand(123456)
						writer = c
					}
					var want []byte
					for _, size := range []int{0, 1, 2, 3, 13, 14, 15, 16, 4097, 65537} {
						p := make([]byte, size)
						for i := range p {
							p[i] = byte(i*31 + i>>8)
						}
						if _, err := writer.Write(p); err != nil {
							t.Fatal(err)
						}
						want = append(want, p...)
					}
					digest := fmt.Sprintf("%x", sha256.Sum256(wire.Bytes()))
					if digest != goldens[name] {
						t.Fatal("encoded bytes differ from the legacy wire sample")
					}
					var reader net.Conn
					raw := &fragmentConn{data: wire.Bytes(), limit: 127}
					if packed {
						reader = NewPackedConn(raw, table, padding, padding)
					} else {
						reader = NewConn(raw, table, padding, padding, false)
					}
					got, err := io.ReadAll(reader)
					if err != nil || !bytes.Equal(got, want) {
						t.Fatalf("wire decode mismatch: %v", err)
					}
				})
			}
		}
	}
}
