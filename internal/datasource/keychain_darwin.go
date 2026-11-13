//go:build darwin && cgo

package datasource

// macOS 钥匙串读取：用 Security.framework 的 SecItemCopyMatching 取浏览器的
// Safe Storage 密码。只读固定 service/account，不执行任何外部命令。
// 首次访问会弹系统授权；用户拒绝时返回可展示的中文错误。

/*
#cgo LDFLAGS: -framework Security -framework CoreFoundation
#include <stdlib.h>
#include <string.h>
#include <Security/Security.h>
#include <CoreFoundation/CoreFoundation.h>

// guanlan_keychain_password 读取通用密码项；成功时把字节写入 malloc 缓冲区。
static OSStatus guanlan_keychain_password(const char *service, const char *account, void **out, int *outLen) {
	CFStringRef svc = CFStringCreateWithCString(kCFAllocatorDefault, service, kCFStringEncodingUTF8);
	CFStringRef acc = CFStringCreateWithCString(kCFAllocatorDefault, account, kCFStringEncodingUTF8);
	if (!svc || !acc) {
		if (svc) CFRelease(svc);
		if (acc) CFRelease(acc);
		return errSecParam;
	}
	CFMutableDictionaryRef query = CFDictionaryCreateMutable(kCFAllocatorDefault, 0,
		&kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	if (!query) {
		CFRelease(svc);
		CFRelease(acc);
		return errSecAllocate;
	}
	CFDictionaryAddValue(query, kSecClass, kSecClassGenericPassword);
	CFDictionaryAddValue(query, kSecAttrService, svc);
	CFDictionaryAddValue(query, kSecAttrAccount, acc);
	CFDictionaryAddValue(query, kSecReturnData, kCFBooleanTrue);
	CFDictionaryAddValue(query, kSecMatchLimit, kSecMatchLimitOne);
	CFTypeRef result = NULL;
	OSStatus status = SecItemCopyMatching(query, &result);
	if (status == errSecSuccess && result) {
		CFDataRef data = (CFDataRef)result;
		CFIndex n = CFDataGetLength(data);
		if (n > 0) {
			void *buf = malloc((size_t)n);
			if (buf) {
				memcpy(buf, CFDataGetBytePtr(data), (size_t)n);
				*out = buf;
				*outLen = (int)n;
			} else {
				status = errSecAllocate;
			}
		}
	}
	if (result) CFRelease(result);
	CFRelease(query);
	CFRelease(svc);
	CFRelease(acc);
	return status;
}

// guanlan_keychain_class 把 OSStatus 归类，避免 Go 侧依赖枚举常量：
// 0=成功 1=找不到 2=用户拒绝/不允许交互 3=其它。
static int guanlan_keychain_class(OSStatus status) {
	if (status == errSecSuccess) return 0;
	if (status == errSecItemNotFound) return 1;
	if (status == errSecUserCanceled || status == errSecInteractionNotAllowed) return 2;
	return 3;
}

static void guanlan_free_bytes(void *p) { free(p); }
*/
import "C"

import (
	"errors"
	"fmt"
	"unsafe"
)

// keychainPassword 从 macOS 钥匙串读取浏览器的 Safe Storage 密码。
func keychainPassword(service, account string) (string, error) {
	cs := C.CString(service)
	ca := C.CString(account)
	defer C.free(unsafe.Pointer(cs))
	defer C.free(unsafe.Pointer(ca))

	var out unsafe.Pointer
	var n C.int
	status := C.guanlan_keychain_password(cs, ca, &out, &n)
	class := int(C.guanlan_keychain_class(status))
	if class != 0 {
		switch class {
		case 1:
			return "", errors.New("钥匙串里没有该浏览器的 Safe Storage 条目")
		case 2:
			return "", errors.New("钥匙串授权被取消：请在系统弹窗里允许访问后重试")
		default:
			return "", fmt.Errorf("读取钥匙串失败（OSStatus %d）", int(status))
		}
	}
	if out == nil || n <= 0 {
		return "", errors.New("钥匙串返回了空密码")
	}
	defer C.guanlan_free_bytes(out)
	return C.GoStringN((*C.char)(out), n), nil
}
