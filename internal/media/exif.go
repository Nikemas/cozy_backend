package media

import (
	"bytes"
	"encoding/binary"
)

// exifOrientation returns the EXIF Orientation tag (0x0112) of an image
// file, or 1 ("already upright") when there is none or it can't be
// parsed. Only this one tag is ever needed, so rather than pulling in an
// EXIF library this walks just enough of each container to find the TIFF
// block and IFD0 in it:
//
//   - JPEG: the APP1 segment starting with "Exif\x00\x00" (phones put the
//     orientation here — the common "upside-down photo" case);
//   - WebP: the RIFF "EXIF" chunk;
//   - PNG: the "eXIf" chunk.
//
// Every read is bounds-checked: the input is an untrusted upload, and a
// malformed EXIF block must degrade to "no rotation", never a panic.
func exifOrientation(data []byte) int {
	var tiff []byte
	switch {
	case len(data) >= 2 && data[0] == 0xFF && data[1] == 0xD8:
		tiff = jpegEXIF(data)
	case len(data) >= 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		tiff = webpEXIF(data)
	case len(data) >= 8 && string(data[0:8]) == "\x89PNG\r\n\x1a\n":
		tiff = pngEXIF(data)
	}
	if tiff == nil {
		return 1
	}
	return tiffOrientation(tiff)
}

var exifHeader = []byte("Exif\x00\x00")

// jpegEXIF scans JPEG marker segments up to the start of scan data and
// returns the TIFF block of the first Exif APP1 segment.
func jpegEXIF(data []byte) []byte {
	i := 2
	for i+4 <= len(data) {
		if data[i] != 0xFF {
			return nil
		}
		marker := data[i+1]
		// Fill bytes / standalone markers without a length field.
		if marker == 0xFF {
			i++
			continue
		}
		if marker == 0xD8 || (marker >= 0xD0 && marker <= 0xD7) || marker == 0x01 {
			i += 2
			continue
		}
		if marker == 0xDA || marker == 0xD9 { // start of scan / end of image
			return nil
		}
		segLen := int(binary.BigEndian.Uint16(data[i+2 : i+4]))
		if segLen < 2 || i+2+segLen > len(data) {
			return nil
		}
		payload := data[i+4 : i+2+segLen]
		if marker == 0xE1 && bytes.HasPrefix(payload, exifHeader) {
			return payload[len(exifHeader):]
		}
		i += 2 + segLen
	}
	return nil
}

// webpEXIF walks the RIFF chunks of a WebP file and returns the payload
// of the "EXIF" chunk. Some writers prefix it with "Exif\x00\x00" like
// JPEG does, so that is stripped too.
func webpEXIF(data []byte) []byte {
	i := 12
	for i+8 <= len(data) {
		fourCC := string(data[i : i+4])
		size := int(binary.LittleEndian.Uint32(data[i+4 : i+8]))
		if size < 0 || i+8+size > len(data) {
			return nil
		}
		if fourCC == "EXIF" {
			return bytes.TrimPrefix(data[i+8:i+8+size], exifHeader)
		}
		i += 8 + size + size%2 // chunks are padded to an even size
	}
	return nil
}

// pngEXIF walks PNG chunks and returns the payload of the "eXIf" chunk.
func pngEXIF(data []byte) []byte {
	i := 8
	for i+8 <= len(data) {
		size := int(binary.BigEndian.Uint32(data[i : i+4]))
		typ := string(data[i+4 : i+8])
		if size < 0 || i+12+size > len(data) {
			return nil
		}
		if typ == "eXIf" {
			return data[i+8 : i+8+size]
		}
		if typ == "IDAT" || typ == "IEND" {
			// eXIf must come before image data to be honored (PNG 1.5
			// extensions spec); anything later is ignored by viewers too.
			return nil
		}
		i += 12 + size // length + type + data + CRC
	}
	return nil
}

// tiffOrientation reads the Orientation tag from IFD0 of a TIFF block
// ("II"/"MM" byte order header, then entries of 12 bytes each).
func tiffOrientation(tiff []byte) int {
	if len(tiff) < 8 {
		return 1
	}
	var bo binary.ByteOrder
	switch string(tiff[0:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 1
	}
	if bo.Uint16(tiff[2:4]) != 42 {
		return 1
	}
	ifd := int(bo.Uint32(tiff[4:8]))
	if ifd < 8 || ifd+2 > len(tiff) {
		return 1
	}
	n := int(bo.Uint16(tiff[ifd : ifd+2]))
	for k := 0; k < n; k++ {
		e := ifd + 2 + k*12
		if e+12 > len(tiff) {
			return 1
		}
		if bo.Uint16(tiff[e:e+2]) != 0x0112 {
			continue
		}
		// Type SHORT (3), count 1: the value sits in the first two bytes
		// of the 4-byte value field.
		if bo.Uint16(tiff[e+2:e+4]) != 3 {
			return 1
		}
		o := int(bo.Uint16(tiff[e+8 : e+10]))
		if o < 1 || o > 8 {
			return 1
		}
		return o
	}
	return 1
}
