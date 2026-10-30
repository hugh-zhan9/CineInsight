//go:build darwin && cgo

package services

/*
#cgo LDFLAGS: -framework Security -framework CoreFoundation
#include <Security/Security.h>
#include <CoreFoundation/CoreFoundation.h>
#include <stdlib.h>
#include <string.h>
#include <stdint.h>
static CFMutableDictionaryRef btQuery(const char *service,const char *ref) {
 CFMutableDictionaryRef q=CFDictionaryCreateMutable(NULL,0,&kCFTypeDictionaryKeyCallBacks,&kCFTypeDictionaryValueCallBacks);
 CFDictionarySetValue(q,kSecClass,kSecClassGenericPassword);
 CFStringRef name=CFStringCreateWithCString(NULL,service,kCFStringEncodingUTF8);CFDictionarySetValue(q,kSecAttrService,name);CFRelease(name);
 if(ref){CFStringRef a=CFStringCreateWithCString(NULL,ref,kCFStringEncodingUTF8);CFDictionarySetValue(q,kSecAttrAccount,a);CFRelease(a);}
 return q;
}
static int btPut(const char *service,const char *ref,const void *bytes,int length){
 CFMutableDictionaryRef q=btQuery(service,ref);CFDataRef data=CFDataCreate(NULL,bytes,length);
 CFDictionarySetValue(q,kSecValueData,data);CFDictionarySetValue(q,kSecAttrAccessible,kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly);
 OSStatus status=SecItemAdd(q,NULL);CFRelease(data);CFRelease(q);return status;
}
static int btGet(const char *service,const char *ref,void **bytes,int *length){
 CFMutableDictionaryRef q=btQuery(service,ref);CFDictionarySetValue(q,kSecReturnData,kCFBooleanTrue);CFDictionarySetValue(q,kSecMatchLimit,kSecMatchLimitOne);
 CFTypeRef result=NULL;OSStatus status=SecItemCopyMatching(q,&result);CFRelease(q);
 if(status==errSecSuccess){CFDataRef data=(CFDataRef)result;*length=(int)CFDataGetLength(data);if(*length>32768){CFRelease(result);return -1;}*bytes=malloc(*length);if(!*bytes && *length){CFRelease(result);return -1;}memcpy(*bytes,CFDataGetBytePtr(data),*length);CFRelease(result);}return status;
}
static int btDelete(const char *service,const char *ref){CFMutableDictionaryRef q=btQuery(service,ref);OSStatus s=SecItemDelete(q);CFRelease(q);return s==errSecItemNotFound?0:s;}
// Accept only this application's ASCII reference format, including malformed rows.
static int btAccount(CFTypeRef row, char out[55]) {
 if(!row || CFGetTypeID(row)!=CFDictionaryGetTypeID())return 0;
 CFTypeRef account=CFDictionaryGetValue((CFDictionaryRef)row,kSecAttrAccount);
 if(!account || CFGetTypeID(account)!=CFStringGetTypeID())return 0;
 CFStringRef text=(CFStringRef)account;
 if(CFStringGetLength(text)!=54 || !CFStringGetCString(text,out,55,kCFStringEncodingUTF8) || strncmp(out,"ci-bt-",6)!=0)return 0;
 for(int i=6;i<54;i++){if(!((out[i]>='0'&&out[i]<='9')||(out[i]>='a'&&out[i]<='f')))return 0;}
 return 1;
}
static int btList(const char *service,char **text){
 CFMutableDictionaryRef q=btQuery(service,NULL);CFDictionarySetValue(q,kSecReturnAttributes,kCFBooleanTrue);CFDictionarySetValue(q,kSecMatchLimit,kSecMatchLimitAll);
 CFTypeRef result=NULL;OSStatus status=SecItemCopyMatching(q,&result);CFRelease(q);if(status==errSecItemNotFound){*text=strdup("");return *text?0:-1;}if(status!=0)return status;
 if(!result || CFGetTypeID(result)!=CFArrayGetTypeID()){if(result)CFRelease(result);return -1;}
 CFArrayRef rows=(CFArrayRef)result;size_t size=1;CFIndex n=CFArrayGetCount(rows);char account[55];
 for(CFIndex i=0;i<n;i++){if(btAccount(CFArrayGetValueAtIndex(rows,i),account)){if(size>SIZE_MAX-55){CFRelease(result);return -1;}size+=55;}}
 *text=calloc(size,1);if(!*text){CFRelease(result);return -1;}
 size_t offset=0;
 for(CFIndex i=0;i<n;i++){if(btAccount(CFArrayGetValueAtIndex(rows,i),account)){memcpy(*text+offset,account,54);offset+=54;(*text)[offset++]='\n';}}
 CFRelease(result);return 0;
}

*/
import "C"
import (
	"os"
	"path/filepath"
	"strings"
	"unsafe"
)

type btKeychainStore struct{ scope string }

func NewBTSystemSecretStore(scope string) BTSecretStore { return btKeychainStore{scope: scope} }
func (s btKeychainStore) service() string {
	if len(s.scope) != 64 {
		return ""
	}
	for _, c := range s.scope {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return ""
		}
	}
	return "CineInsight.BT." + s.scope
}
func (s btKeychainStore) TryLock() (func(), error) {
	if s.service() == "" {
		return nil, ErrBTCredentials
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return nil, ErrBTCredentials
	}
	return (BTFileCredentialLock{Path: filepath.Join(cache, "CineInsight", "bt-credentials", s.scope+".lock")}).TryLock()
}

func btValidRef(ref string) bool {
	if len(ref) != 54 || !strings.HasPrefix(ref, "ci-bt-") {
		return false
	}
	for _, c := range ref[6:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func (s btKeychainStore) Put(ref string, data []byte) error {
	if s.service() == "" || !btValidRef(ref) || len(data) == 0 || len(data) > 32768 {
		return ErrBTCredentials
	}
	service := C.CString(s.service())
	defer C.free(unsafe.Pointer(service))
	r := C.CString(ref)
	defer C.free(unsafe.Pointer(r))
	if C.btPut(service, r, unsafe.Pointer(&data[0]), C.int(len(data))) != 0 {
		return ErrBTCredentials
	}
	return nil
}
func (s btKeychainStore) Get(ref string) ([]byte, error) {
	if s.service() == "" || !btValidRef(ref) {
		return nil, ErrBTCredentials
	}
	service := C.CString(s.service())
	defer C.free(unsafe.Pointer(service))
	r := C.CString(ref)
	defer C.free(unsafe.Pointer(r))
	var data unsafe.Pointer
	var n C.int
	if C.btGet(service, r, &data, &n) != 0 {
		return nil, ErrBTCredentials
	}
	defer C.free(data)
	return C.GoBytes(data, n), nil
}
func (s btKeychainStore) Delete(ref string) error {
	if s.service() == "" || !btValidRef(ref) {
		return ErrBTCredentials
	}
	service := C.CString(s.service())
	defer C.free(unsafe.Pointer(service))
	r := C.CString(ref)
	defer C.free(unsafe.Pointer(r))
	if C.btDelete(service, r) != 0 {
		return ErrBTCredentials
	}
	return nil
}
func (s btKeychainStore) List() ([]string, error) {
	if s.service() == "" {
		return nil, ErrBTCredentials
	}
	service := C.CString(s.service())
	defer C.free(unsafe.Pointer(service))
	var text *C.char
	if C.btList(service, &text) != 0 {
		return nil, ErrBTCredentials
	}
	defer C.free(unsafe.Pointer(text))
	out := []string{}
	for _, r := range strings.Split(C.GoString(text), "\n") {
		if btValidRef(r) {
			out = append(out, r)
		}
	}
	return out, nil
}
