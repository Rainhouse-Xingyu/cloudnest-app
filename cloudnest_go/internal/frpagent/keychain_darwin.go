//go:build darwin && cgo

package frpagent

/*
#cgo CFLAGS: -Wno-deprecated-declarations
#cgo LDFLAGS: -framework Security -framework CoreFoundation
#include <Security/Security.h>
#include <CoreFoundation/CoreFoundation.h>
#include <stdlib.h>
#include <string.h>

static OSStatus cn_default_keychain(char *path) {
    SecKeychainRef chain=NULL;
    OSStatus status=SecKeychainCopyDefault(&chain);
    if(status!=errSecSuccess) return status;
    UInt32 length=4096;
    status=SecKeychainGetPath(chain,&length,path);
    CFRelease(chain);
    return status;
}

// The foreground CLI uses a file-based macOS Keychain. Do not change the
// default keychain/search list and do not permit interactive prompts in a
// background Agent. SecItem handles secret data in-process, never argv/env.
static OSStatus cn_keychain_key(const char *path, const char *account, unsigned char *key, int create) {
    SecKeychainRef chain = NULL;
    OSStatus status = SecKeychainSetUserInteractionAllowed(false);
    if (status != errSecSuccess) return status;
    status = path[0] ? SecKeychainOpen(path, &chain) : SecKeychainCopyDefault(&chain);
    if (status != errSecSuccess) return status;
    SecKeychainStatus state = 0;
    status = SecKeychainGetStatus(chain, &state);
    if (status != errSecSuccess || !(state & kSecUnlockStateStatus)) {
        CFRelease(chain);
        return status != errSecSuccess ? status : errSecInteractionNotAllowed;
    }
    CFMutableDictionaryRef query = CFDictionaryCreateMutable(NULL,0,&kCFTypeDictionaryKeyCallBacks,&kCFTypeDictionaryValueCallBacks);
    CFStringRef name = CFStringCreateWithCString(NULL,account,kCFStringEncodingUTF8);
    CFDictionarySetValue(query,kSecClass,kSecClassGenericPassword);
    CFDictionarySetValue(query,kSecAttrService,CFSTR("com.cloudnest.agent.vault.v1"));
    CFDictionarySetValue(query,kSecAttrAccount,name);
    if (create) {
        CFDataRef data = CFDataCreate(NULL,key,32);
        CFDictionarySetValue(query,kSecUseKeychain,chain);
        CFDictionarySetValue(query,kSecAttrLabel,CFSTR("CloudNest Agent encryption key"));
        CFDictionarySetValue(query,kSecValueData,data);
        status = SecItemAdd(query,NULL);
        CFRelease(data);
    } else {
        const void *value = chain;
        CFArrayRef search = CFArrayCreate(NULL,&value,1,&kCFTypeArrayCallBacks);
        CFDictionarySetValue(query,kSecMatchSearchList,search);
        CFDictionarySetValue(query,kSecMatchLimit,kSecMatchLimitOne);
        CFDictionarySetValue(query,kSecReturnData,kCFBooleanTrue);
        CFTypeRef result = NULL;
        status = SecItemCopyMatching(query,&result);
        if (status == errSecSuccess) {
            if (!result || CFGetTypeID(result) != CFDataGetTypeID() || CFDataGetLength((CFDataRef)result) != 32) status=errSecDecode;
            else memcpy(key,CFDataGetBytePtr((CFDataRef)result),32);
        }
        if (result) CFRelease(result);
        CFRelease(search);
    }
    CFRelease(name);CFRelease(query);CFRelease(chain);
    return status;
}
*/
import "C"

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"unsafe"
)

func defaultKeychainPath() (string, error) {
	buf := make([]byte, 4096)
	status := C.cn_default_keychain((*C.char)(unsafe.Pointer(&buf[0])))
	if status != 0 {
		return "", fmt.Errorf("%w (Keychain status %d)", ErrVault, int32(status))
	}
	n := bytes.IndexByte(buf, 0)
	if n <= 0 {
		return "", ErrVault
	}
	path := string(buf[:n])
	if !filepath.IsAbs(path) {
		return "", ErrVault
	}
	return path, nil
}

func keychainKey(path, id string, key []byte, create bool) error {
	if len(key) != 32 || !validID(id) || len(path) > 4096 || strings.ContainsRune(path, 0) {
		return ErrVault
	}
	cp, ci := C.CString(path), C.CString(id)
	defer C.free(unsafe.Pointer(cp))
	defer C.free(unsafe.Pointer(ci))
	mode := C.int(0)
	if create {
		mode = 1
	}
	status := C.cn_keychain_key(cp, ci, (*C.uchar)(unsafe.Pointer(&key[0])), mode)
	if status != 0 {
		return fmt.Errorf("%w (Keychain status %d)", ErrVault, int32(status))
	}
	return nil
}
