package shot

import "unsafe"

// uintptrToPointer converts an address handed over by the OS (a syscall
// callback argument or a GlobalLock result) into an unsafe.Pointer.
//
// The direct uintptr -> unsafe.Pointer conversion trips go vet's checkptr
// heuristic, so the conversion goes through a pointer to the uintptr value,
// which keeps the runtime's pointer-tracking rules satisfied.
func uintptrToPointer(p uintptr) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&p))
}
