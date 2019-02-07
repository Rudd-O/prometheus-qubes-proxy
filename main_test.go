package main

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"testing"
)

func file(t *testing.T, n string) string {
	f, err := ioutil.ReadFile(n)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return string(f)
}

func TestProxyReaderSample(t *testing.T) {
	texts := []string{"arsnetinarsitoniarstarstarst", file(t, "smallfixture.txt")}
	for _, text := range texts {
		input := bytes.NewBuffer([]byte(fmt.Sprintf("%d\n%s", len(text), text)))
		expected := bytes.NewBuffer([]byte(text))
		result := bytes.NewBuffer(make([]byte, 8192))
		p := newProxyReader(input)
		result.Reset()
		nr, err := io.Copy(result, p)
		if err != nil {
			t.Fatalf("%s", err)
		}
		t.Log("Actual bytes read", nr)
		expectedb, err := ioutil.ReadAll(expected)
		if err != nil {
			panic(err)
		}
		resultb, err := ioutil.ReadAll(result)
		if err != nil {
			panic(err)
		}
		t.Log("Expected vs actual len", len(expectedb), len(resultb))
		if len(expectedb) != len(resultb) {
			t.Fatalf("len: %v != %v", len(expectedb), len(resultb))
		}
		if bytes.Compare(expectedb, resultb) != 0 {
			t.Fatalf("strings differ: %s != %s", expectedb, resultb)
		}
	}
}
