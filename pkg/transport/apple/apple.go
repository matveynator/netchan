//go:build darwin && (amd64 || arm64) && !tinygo

// Package apple confines native Objective-C calls to platform adapters.
package apple

import (
	"errors"
	"runtime"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

func Load(framework string) (uintptr, error) {
	return purego.Dlopen("/System/Library/Frameworks/"+framework+".framework/"+framework, purego.RTLD_NOW|purego.RTLD_GLOBAL)
}
func Select(name string) objc.SEL { return objc.RegisterName(name) }
func String(text string) objc.ID {
	return objc.ID(objc.GetClass("NSString")).Send(Select("stringWithUTF8String:"), text)
}
func Text(object objc.ID) string {
	if object == 0 {
		return ""
	}
	pointer := objc.Send[unsafe.Pointer](object, Select("UTF8String"))
	if pointer == nil {
		return ""
	}
	length := objc.Send[uintptr](object, Select("lengthOfBytesUsingEncoding:"), uintptr(4))
	if length > 4096 {
		return ""
	}
	return string(unsafe.Slice((*byte)(pointer), int(length))) // #nosec G103 -- NSString supplies a live UTF-8 buffer of the checked byte length; string copies it.
}
func Array(objects ...objc.ID) objc.ID {
	array := objc.ID(objc.GetClass("NSMutableArray")).Send(Select("array"))
	for _, object := range objects {
		if object != 0 {
			array.Send(Select("addObject:"), object)
		}
	}
	return array
}
func Dictionary(entries map[string]objc.ID) objc.ID {
	dictionary := objc.ID(objc.GetClass("NSMutableDictionary")).Send(Select("dictionary"))
	for key, object := range entries {
		dictionary.Send(Select("setObject:forKey:"), object, String(key))
	}
	return dictionary
}
func Count(array objc.ID) int { return int(objc.Send[uintptr](array, Select("count"))) }
func At(array objc.ID, index int) objc.ID {
	return array.Send(Select("objectAtIndex:"), uintptr(index))
}
func UUID(text string) objc.ID {
	return objc.ID(objc.GetClass("CBUUID")).Send(Select("UUIDWithString:"), String(text))
}
func UUIDText(object objc.ID) string { return Text(object.Send(Select("UUIDString"))) }
func Data(payload []byte) objc.ID {
	object := objc.ID(objc.GetClass("NSData")).Send(Select("dataWithBytes:length:"), unsafe.SliceData(payload), uintptr(len(payload))) // #nosec G103 -- NSData copies exactly len(payload) bytes synchronously; KeepAlive retains the source.
	runtime.KeepAlive(payload)
	return object
}
func Bytes(object objc.ID, maximum int) ([]byte, error) {
	if object == 0 {
		return nil, nil
	}
	length := objc.Send[uintptr](object, Select("length"))
	if length > uintptr(maximum) {
		return nil, errors.New("netchan: oversized native Bluetooth payload")
	}
	if length == 0 {
		return nil, nil
	}
	pointer := objc.Send[unsafe.Pointer](object, Select("bytes"))
	if pointer == nil {
		return nil, errors.New("netchan: missing native Bluetooth bytes")
	}
	return append([]byte(nil), unsafe.Slice((*byte)(pointer), int(length))...), nil // #nosec G103 -- NSData owns the live checked-length buffer; append copies it before the native object can be released.
}
func Error(object objc.ID) error {
	if object == 0 {
		return nil
	}
	return errors.New(Text(object.Send(Select("localizedDescription"))))
}
func Pool() func() {
	pool := objc.ID(objc.GetClass("NSAutoreleasePool")).Send(Select("new"))
	return func() { pool.Send(Select("drain")) }
}
