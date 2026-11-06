//go:build darwin && cgo && keychainnative

package frpagent

/*
#cgo CFLAGS: -Wno-deprecated-declarations
#cgo LDFLAGS: -framework Security -framework CoreFoundation
#include <Security/Security.h>
#include <stdlib.h>
static OSStatus cn_test_chain(const char *path,const unsigned char *password,unsigned int length,int operation) {
    SecKeychainRef chain=NULL;
    OSStatus status;
    if (operation==0) {
        status=SecKeychainCreate(path,length,password,false,NULL,&chain);
        if(status==errSecSuccess) status=SecKeychainUnlock(chain,length,password,true);
    } else {
        status=SecKeychainOpen(path,&chain);
        if(status==errSecSuccess) {
            if(operation==1) status=SecKeychainLock(chain);
            else if(operation==2) status=SecKeychainUnlock(chain,length,password,true);
            else if(operation==3) status=SecKeychainDelete(chain);
        }
    }
    if(chain) CFRelease(chain);
    return status;
}
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// These helpers are excluded from normal Agent builds. Only synthetic keys in
// a test-owned temporary keychain are created, locked, unlocked and deleted.
func testKeychainOperation(path string, password []byte, operation int) error {
	p := C.CString(path)
	defer C.free(unsafe.Pointer(p))
	status := C.cn_test_chain(p, (*C.uchar)(unsafe.Pointer(&password[0])), C.uint(len(password)), C.int(operation))
	if status != 0 {
		return fmt.Errorf("isolated Keychain operation status %d", int32(status))
	}
	return nil
}
