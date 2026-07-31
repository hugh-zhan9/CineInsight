package services

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseLocalMovieNFOMapsApprovedFieldsOnly(t *testing.T) {
	document, err := parseLocalMovieNFO([]byte(`<?xml version="1.0"?>
<movie>
  <title>  显示标题  </title>
  <originaltitle>Original Title</originaltitle>
  <plot>主要简介</plot><outline>备用简介</outline>
  <actor><name> Alice </name><thumb>https://example.invalid/alice.jpg</thumb></actor>
  <actor><name>Alice</name></actor><actor><name>Bob</name></actor>
  <set><name>系列 A</name></set>
  <playcount>99</playcount><resume><position>123</position></resume>
  <fileinfo><streamdetails><video><width>9999</width></video></streamdetails></fileinfo>
</movie>`))
	if err != nil {
		t.Fatalf("parse valid NFO: %v", err)
	}
	if document.Title != "显示标题" || document.OriginalTitle != "Original Title" || document.Description != "主要简介" {
		t.Fatalf("mapped document = %#v", document)
	}
	if len(document.People) != 2 || document.People[0] != "Alice" || document.People[1] != "Bob" || document.Collection != "系列 A" {
		t.Fatalf("mapped relations = %#v", document)
	}
}

func TestParseLocalMovieNFORejectsUnsafeOrUnboundedXML(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{name: "doctype", content: `<!DOCTYPE movie [<!ENTITY x "bad">]><movie><title>&x;</title></movie>`},
		{name: "processing instruction", content: `<movie><?target value?><title>x</title></movie>`},
		{name: "wrong root", content: `<tvshow><title>x</title></tvshow>`},
		{name: "deep", content: `<movie>` + strings.Repeat(`<x>`, localMetadataXMLMaxDepth) + strings.Repeat(`</x>`, localMetadataXMLMaxDepth) + `</movie>`},
		{name: "text", content: `<movie><title>` + strings.Repeat("x", localMetadataTextMaxBytes+1) + `</title></movie>`},
	}
	actors := strings.Builder{}
	actors.WriteString(`<movie>`)
	for index := 0; index <= localMetadataActorMaxCount; index++ {
		actors.WriteString(fmt.Sprintf(`<actor><name>actor-%d</name></actor>`, index))
	}
	actors.WriteString(`</movie>`)
	tests = append(tests, struct {
		name    string
		content string
	}{name: "actors", content: actors.String()})

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseLocalMovieNFO([]byte(test.content)); err == nil {
				t.Fatal("expected parser rejection")
			}
		})
	}
}
