package services

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	localMetadataNFOMaxBytes   int64 = 4 << 20
	localMetadataXMLMaxDepth         = 64
	localMetadataTextMaxBytes        = 64 << 10
	localMetadataActorMaxCount       = 1000
)

type localMovieNFO struct {
	XMLName       xml.Name `xml:"movie"`
	Title         string   `xml:"title"`
	OriginalTitle string   `xml:"originaltitle"`
	Plot          string   `xml:"plot"`
	Outline       string   `xml:"outline"`
	Actors        []struct {
		Name string `xml:"name"`
	} `xml:"actor"`
	Set struct {
		Name string `xml:"name"`
	} `xml:"set"`
}

type LocalMetadataDocument struct {
	Title         string   `json:"title"`
	OriginalTitle string   `json:"original_title"`
	Description   string   `json:"description"`
	People        []string `json:"people"`
	Collection    string   `json:"collection"`
}

func parseLocalMovieNFO(content []byte) (LocalMetadataDocument, error) {
	if int64(len(content)) > localMetadataNFOMaxBytes {
		return LocalMetadataDocument{}, fmt.Errorf("nfo exceeds %d byte limit", localMetadataNFOMaxBytes)
	}
	decoder := xml.NewDecoder(bytes.NewReader(content))
	decoder.Strict = true
	depth := 0
	rootSeen := false
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return LocalMetadataDocument{}, fmt.Errorf("parse nfo xml: %w", err)
		}
		switch value := token.(type) {
		case xml.StartElement:
			depth++
			if depth > localMetadataXMLMaxDepth {
				return LocalMetadataDocument{}, fmt.Errorf("nfo xml exceeds depth %d", localMetadataXMLMaxDepth)
			}
			if !rootSeen {
				rootSeen = true
				if !strings.EqualFold(value.Name.Local, "movie") {
					return LocalMetadataDocument{}, errors.New("nfo root must be movie")
				}
			}
		case xml.EndElement:
			depth--
		case xml.Directive:
			return LocalMetadataDocument{}, errors.New("nfo xml directives are not allowed")
		case xml.ProcInst:
			if !strings.EqualFold(value.Target, "xml") || rootSeen {
				return LocalMetadataDocument{}, errors.New("nfo processing instructions are not allowed")
			}
		}
	}
	if !rootSeen {
		return LocalMetadataDocument{}, errors.New("nfo is empty")
	}

	var source localMovieNFO
	decoder = xml.NewDecoder(bytes.NewReader(content))
	decoder.Strict = true
	if err := decoder.Decode(&source); err != nil {
		return LocalMetadataDocument{}, fmt.Errorf("decode nfo: %w", err)
	}
	if !strings.EqualFold(source.XMLName.Local, "movie") {
		return LocalMetadataDocument{}, errors.New("nfo root must be movie")
	}
	if len(source.Actors) > localMetadataActorMaxCount {
		return LocalMetadataDocument{}, fmt.Errorf("nfo exceeds %d actor limit", localMetadataActorMaxCount)
	}
	fields := []string{source.Title, source.OriginalTitle, source.Plot, source.Outline, source.Set.Name}
	for _, actor := range source.Actors {
		fields = append(fields, actor.Name)
	}
	for _, field := range fields {
		if len(field) > localMetadataTextMaxBytes {
			return LocalMetadataDocument{}, fmt.Errorf("nfo text field exceeds %d byte limit", localMetadataTextMaxBytes)
		}
	}

	description := strings.TrimSpace(source.Plot)
	if description == "" {
		description = strings.TrimSpace(source.Outline)
	}
	people := make([]string, 0, len(source.Actors))
	seenPeople := make(map[string]struct{}, len(source.Actors))
	for _, actor := range source.Actors {
		name := strings.TrimSpace(actor.Name)
		normalized := normalizeLocalMetadataName(name)
		if normalized == "" {
			continue
		}
		if _, exists := seenPeople[normalized]; exists {
			continue
		}
		seenPeople[normalized] = struct{}{}
		people = append(people, name)
	}
	return LocalMetadataDocument{
		Title:         strings.TrimSpace(source.Title),
		OriginalTitle: strings.TrimSpace(source.OriginalTitle),
		Description:   description,
		People:        people,
		Collection:    strings.TrimSpace(source.Set.Name),
	}, nil
}

func normalizeLocalMetadataName(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
}
