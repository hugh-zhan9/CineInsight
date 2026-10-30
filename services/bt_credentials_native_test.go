//go:build darwin && cgo

package services

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise the actual native account filter without reading or changing Keychain.
func TestBTKeychainMalformedAttributes(t *testing.T) {
	source, err := os.ReadFile("bt_credentials_darwin.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	start := strings.Index(text, "static int btAccount(")
	end := strings.Index(text, "static int btList(")
	if start < 0 || end <= start {
		t.Fatal("native account helper missing")
	}
	program := `#include <Security/Security.h>
#include <CoreFoundation/CoreFoundation.h>
#include <string.h>
` + text[start:end] + `
int main(void) {
 char output[55];
 if(btAccount(NULL,output))return 1;
 if(btAccount(CFSTR("not a dictionary"),output))return 2;
 CFMutableDictionaryRef row=CFDictionaryCreateMutable(NULL,0,&kCFTypeDictionaryKeyCallBacks,&kCFTypeDictionaryValueCallBacks);
 if(btAccount(row,output))return 3;
 CFDictionarySetValue(row,kSecAttrAccount,kCFBooleanTrue);
 if(btAccount(row,output))return 4;
 CFDictionarySetValue(row,kSecAttrAccount,CFSTR("foreign-account"));
 if(btAccount(row,output))return 5;
 CFDictionarySetValue(row,kSecAttrAccount,CFSTR("ci-bt-0123456789abcdef0123456789abcdef0123456789abcdef"));
 if(!btAccount(row,output))return 6;
 if(strcmp(output,"ci-bt-0123456789abcdef0123456789abcdef0123456789abcdef"))return 7;
 CFRelease(row);return 0;
}
`
	dir := t.TempDir()
	src, binary := filepath.Join(dir, "account.c"), filepath.Join(dir, "account")
	if err := os.WriteFile(src, []byte(program), 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("clang", src, "-framework", "CoreFoundation", "-framework", "Security", "-o", binary).CombinedOutput(); err != nil {
		t.Fatalf("compile native filter: %v %s", err, output)
	}
	if output, err := exec.Command(binary).CombinedOutput(); err != nil {
		t.Fatalf("malformed native attributes: %v %s", err, output)
	}
}
