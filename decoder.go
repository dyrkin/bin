package bin

import (
	"bytes"
	"encoding/binary"
	"io"

	"reflect"
	"strconv"

	"github.com/dyrkin/bin/util"
)

type decoder struct {
	buf *bytes.Buffer
}

//Decode struct from byte array
func Decode(payload []byte, response interface{}) {
	value := reflect.ValueOf(response)
	decoder := &decoder{bytes.NewBuffer(payload)}
	decoder.decode(value)
}

func (d *decoder) decode(value reflect.Value) {
	switch value.Kind() {
	case reflect.Ptr:
		d.pointer(value)
	case reflect.Struct:
		d.strukt(value)
	}
}

func deserialize(value reflect.Value, r io.Reader) {
	value.MethodByName("Deserialize").Call([]reflect.Value{reflect.ValueOf(r)})
}

func (d *decoder) pointer(value reflect.Value) {
	if value.IsNil() {
		element := reflect.New(value.Type().Elem())
		if value.CanSet() {
			value.Set(element)
		}
	}
	if value.Type().Implements(serializable) {
		deserialize(value, d.buf)
	} else {
		d.decode(value.Elem())
	}
}

func (d *decoder) strukt(value reflect.Value) {
	var bitmaskBytes uint64
	for i := 0; i < value.NumField(); i++ {
		field := value.Field(i)
		fieldType := value.Type().Field(i)
		tags := tags(fieldType.Tag)
		if !(tags.transient() == "true") && checkConditions(tags.cond(), value) {
			switch field.Kind() {
			case reflect.Ptr:
				d.pointer(field)
			case reflect.String:
				d.string(field, tags)
			case reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
				d.uint(field, tags, &bitmaskBytes)
			case reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
				d.int(field, tags)
			case reflect.Array:
				d.array(field, tags)
			case reflect.Slice:
				d.slice(value, field, tags)
			}
		}
	}
}

func (d *decoder) slice(parent reflect.Value, value reflect.Value, tags tags) {
	if tags.size().nonEmpty() {
		length := d.dynamicLength(tags)
		value.Set(reflect.MakeSlice(value.Type(), length, length))
		for i := 0; i < length; i++ {
			sliceElement := value.Index(i)
			switch sliceElement.Kind() {
			case reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
				d.uint(sliceElement, tags, nil)
			case reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
				d.int(sliceElement, tags)
			case reflect.String:
				d.string(sliceElement, tags)
			case reflect.Ptr:
				d.pointer(sliceElement)
			case reflect.Struct:
				d.strukt(sliceElement)
			}
		}
	} else {
		value.Set(reflect.MakeSlice(value.Type(), 0, 0))
		for {
			if d.buf.Len() == 0 {
				return
			}
			sliceElement := reflect.New(value.Type().Elem()).Elem()
			switch sliceElement.Kind() {
			case reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
				d.uint(sliceElement, tags, nil)
			case reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
				d.int(sliceElement, tags)
			case reflect.String:
				d.string(sliceElement, tags)
			case reflect.Ptr:
				d.pointer(sliceElement)
			case reflect.Struct:
				d.strukt(sliceElement)
			}
			value.Set(reflect.Append(value, sliceElement))
		}
	}
}

func (d *decoder) array(value reflect.Value, tags tags) {
	if value.Len() > 0 {
		size := int(value.Index(0).Type().Size())
		for i := 0; i < value.Len(); i++ {
			arrayElem := value.Index(i)
			v := d.readUint(tags.endianness(), size)
			arrayElem.SetUint(v)
		}
	}
}

func (d *decoder) uint(value reflect.Value, tags tags, bitmaskBytes *uint64) {
	if value.CanAddr() {
		if tags.bits().nonEmpty() {
			if tags.bitmask() == "start" {
				*bitmaskBytes = d.readUint(tags.endianness(), int(value.Type().Size()))
			}
			bitmaskBits := bitmaskBits(tags.bits())
			pos := util.FirstBitPosition(bitmaskBits)
			v := (*bitmaskBytes & bitmaskBits) >> pos
			value.SetUint(v)
		} else if tags.bound().nonEmpty() {
			size, _ := strconv.Atoi(string(tags.bound()))
			v := d.readUint(tags.endianness(), size)
			value.SetUint(v)
		} else {
			v := d.readUint(tags.endianness(), int(value.Type().Size()))
			value.SetUint(v)
		}
	} else {
		panic("Unaddressable uint value")
	}
}

func (d *decoder) int(value reflect.Value, tags tags) {
	if value.CanAddr() {
		if tags.bound().nonEmpty() {
			size, _ := strconv.Atoi(string(tags.bound()))
			v := d.readInt(tags.endianness(), size)
			value.SetInt(v)
		} else {
			v := d.readInt(tags.endianness(), int(value.Type().Size()))
			value.SetInt(v)
		}
	} else {
		panic("Unaddressable uint value")
	}
}

func (d *decoder) string(value reflect.Value, tags tags) {
	if tags.hex().nonEmpty() {
		size, _ := strconv.Atoi(string(tags.hex()))
		v := d.readUint(tags.endianness(), size)
		hexString, _ := util.UintToHexString(v, size)
		value.SetString(hexString)
	} else {
		length := d.dynamicLength(tags)
		b := make([]uint8, length, length)
		d.read(tags.endianness(), b)
		value.SetString(string(b))
	}
}

func (d *decoder) dynamicLength(tags tags) int {
	if tags.size().nonEmpty() {
		size, _ := strconv.Atoi(string(tags.size()))
		return int(d.readUint(tags.endianness(), size))
	}
	return len(d.buf.Bytes())
}

func (d *decoder) read(endianness tag, v interface{}) {
	binary.Read(d.buf, order(endianness), v)
}

func (d *decoder) readUint(endianness tag, size int) uint64 {
	var v uint64
	if endianness == "be" {
		for i := 0; i < size; i++ {
			t, _ := d.buf.ReadByte()
			v = v | uint64(t)<<byte((size-i-1)*8)
		}
	} else {
		for i := 0; i < size; i++ {
			t, _ := d.buf.ReadByte()
			v = v | uint64(t)<<byte(i*8)
		}
	}
	return v
}

func (d *decoder) readInt(endianness tag, size int) int64 {
	var v int64
	buf := make([]uint8, size)
	d.buf.Read(buf)
	if endianness == "be" {
		for i := 0; i < size; i++ {
			t := buf[i]
			if i == 0 {
				v = v | int64(int8(t))<<byte((size-i-1)*8)
			} else {
				v = v | int64(t)<<byte((size-i-1)*8)
			}
		}
	} else {
		for i := 0; i < size; i++ {
			t := buf[i]
			if i != 0 {
				v = v | int64(int8(t))<<byte(i*8)
			} else {
				v = v | int64(t)<<byte(i*8)
			}
		}
	}
	return v
}
