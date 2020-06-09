package objectstorage

import (
	"fmt"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/iotaledger/hive.go/typeutils"

	"github.com/iotaledger/hive.go/platform"
	"github.com/iotaledger/hive.go/reflect"
)

// region interfaces ///////////////////////////////////////////////////////////////////////////////////////////////////

type LeakDetectionWrapper interface {
	CachedObject

	Base() *CachedObjectImpl
	GetInternalId() int64
	SetRetainCallStack(callStack *reflect.CallStack)
	GetRetainCallStack() *reflect.CallStack
	GetRetainTime() time.Time
	SetReleaseCallStack(callStack *reflect.CallStack)
	GetReleaseCallStack() *reflect.CallStack
	WasReleased() bool
}

// endregion ///////////////////////////////////////////////////////////////////////////////////////////////////////////

// region interface implementations ////////////////////////////////////////////////////////////////////////////////////

type LeakDetectionWrapperImpl struct {
	*CachedObjectImpl

	internalId       int64
	retainTime       time.Time
	released         int32
	retainCallStack  *reflect.CallStack
	releaseCallStack *reflect.CallStack
}

var internalIdCounter int64

func newLeakDetectionWrapperImpl(cachedObject *CachedObjectImpl) LeakDetectionWrapper {
	return &LeakDetectionWrapperImpl{
		CachedObjectImpl: cachedObject,
		internalId:       atomic.AddInt64(&internalIdCounter, 1),
	}
}

func (wrappedCachedObject *LeakDetectionWrapperImpl) GetInternalId() int64 {
	return wrappedCachedObject.internalId
}

func (wrappedCachedObject *LeakDetectionWrapperImpl) Base() *CachedObjectImpl {
	return wrappedCachedObject.CachedObjectImpl
}

func (wrappedCachedObject *LeakDetectionWrapperImpl) Consume(consumer func(StorableObject), forceRelease ...bool) bool {
	defer wrappedCachedObject.Release(forceRelease...)

	if storableObject := wrappedCachedObject.CachedObjectImpl.Get(); !typeutils.IsInterfaceNil(storableObject) && !storableObject.IsDeleted() {
		consumer(storableObject)

		return true
	}

	return false
}

func (wrappedCachedObject *LeakDetectionWrapperImpl) Retain() CachedObject {
	baseCachedObject := wrappedCachedObject.CachedObjectImpl
	baseCachedObject.Retain()

	result := wrapCachedObject(baseCachedObject, 0).(*LeakDetectionWrapperImpl)
	result.GetRetainCallStack()

	return result
}

func (wrappedCachedObject *LeakDetectionWrapperImpl) retain() CachedObject {
	baseCachedObject := wrappedCachedObject.CachedObjectImpl
	baseCachedObject.retain()

	result := wrapCachedObject(baseCachedObject, 0).(*LeakDetectionWrapperImpl)
	result.GetRetainCallStack()

	return result
}

func (wrappedCachedObject *LeakDetectionWrapperImpl) Release(force ...bool) {
	if atomic.AddInt32(&(wrappedCachedObject.released), 1) != 1 {
		reportCachedObjectClosedTooOften(wrappedCachedObject, reflect.GetExternalCallers("objectstorage", 0))
	} else {
		baseCachedObject := wrappedCachedObject.CachedObjectImpl

		wrappedCachedObject.SetReleaseCallStack(reflect.GetExternalCallers("objectstorage", 0))
		registerCachedObjectReleased(wrappedCachedObject, baseCachedObject.objectStorage.options.leakDetectionOptions)

		baseCachedObject.Release(force...)
	}
}

func (wrappedCachedObject *LeakDetectionWrapperImpl) GetRetainTime() time.Time {
	return wrappedCachedObject.retainTime
}

func (wrappedCachedObject *LeakDetectionWrapperImpl) SetRetainCallStack(retainCallStack *reflect.CallStack) {
	wrappedCachedObject.retainCallStack = retainCallStack
	wrappedCachedObject.retainTime = time.Now()
}

func (wrappedCachedObject *LeakDetectionWrapperImpl) GetRetainCallStack() *reflect.CallStack {
	return wrappedCachedObject.retainCallStack
}

func (wrappedCachedObject *LeakDetectionWrapperImpl) SetReleaseCallStack(releaseCallStack *reflect.CallStack) {
	wrappedCachedObject.releaseCallStack = releaseCallStack
}

func (wrappedCachedObject *LeakDetectionWrapperImpl) GetReleaseCallStack() *reflect.CallStack {
	return wrappedCachedObject.releaseCallStack
}

func (wrappedCachedObject *LeakDetectionWrapperImpl) WasReleased() bool {
	return atomic.LoadInt32(&wrappedCachedObject.released) != 0
}

// endregion ///////////////////////////////////////////////////////////////////////////////////////////////////////////

// region public API ///////////////////////////////////////////////////////////////////////////////////////////////////

var (
	messageChan           = make(chan interface{})
	instanceRegister      = make(map[string]map[int64]LeakDetectionWrapper)
	instanceRegisterMutex = sync.Mutex{}
)

func init() {
	go func() {
		for {
			if message, isString := (<-messageChan).(string); isString {
				fmt.Println(message)
			} else {
				os.Exit(-1)
			}
		}
	}()
}

var LeakDetection = struct {
	WrapCachedObject                 func(cachedObject *CachedObjectImpl, skipCallerFrames int) CachedObject
	ReportCachedObjectClosedTooOften func(wrappedCachedObject LeakDetectionWrapper, secondCallStack *reflect.CallStack)
	MonitorCachedObjectReleased      func(wrappedCachedObject LeakDetectionWrapper, options *LeakDetectionOptions)
	RegisterCachedObjectRetained     func(wrappedCachedObject LeakDetectionWrapper, options *LeakDetectionOptions)
	RegisterCachedObjectReleased     func(wrappedCachedObject LeakDetectionWrapper, options *LeakDetectionOptions)
}{
	WrapCachedObject:                 wrapCachedObject,
	ReportCachedObjectClosedTooOften: reportCachedObjectClosedTooOften,
	MonitorCachedObjectReleased:      monitorCachedObjectReleased,
	RegisterCachedObjectRetained:     registerCachedObjectRetained,
	RegisterCachedObjectReleased:     registerCachedObjectReleased,
}

func wrapCachedObject(baseCachedObject *CachedObjectImpl, skipCallerFrames int) CachedObject {
	if baseCachedObject == nil {
		return nil
	}

	options := baseCachedObject.objectStorage.options

	if wrapCachedObject := options.leakDetectionWrapper; wrapCachedObject != nil {
		wrappedCachedObject := wrapCachedObject(baseCachedObject)
		wrappedCachedObject.SetRetainCallStack(reflect.GetExternalCallers("objectstorage", skipCallerFrames))

		registerCachedObjectRetained(wrappedCachedObject, options.leakDetectionOptions)
		monitorCachedObjectReleased(wrappedCachedObject, options.leakDetectionOptions)

		return wrappedCachedObject
	}

	return baseCachedObject
}

func reportCachedObjectClosedTooOften(wrappedCachedObject LeakDetectionWrapper, secondCallStack *reflect.CallStack) {
	retainCallStack := wrappedCachedObject.GetRetainCallStack()
	releaseCallStack := wrappedCachedObject.GetReleaseCallStack()

	messageChan <- "[objectstorage::leakkdetection] CachedObject released too often:" + platform.LineBreak +
		"\tretained: " + retainCallStack.ExternalEntryPoint() + platform.LineBreak +
		"\treleased (1): " + releaseCallStack.ExternalEntryPoint() + platform.LineBreak +
		"\treleased (2): " + secondCallStack.ExternalEntryPoint() + platform.LineBreak +
		platform.LineBreak +
		"\tretain call stack (full):" + platform.LineBreak +
		retainCallStack.String() + platform.LineBreak +
		"\trelease call stack (1/2 full):" + platform.LineBreak +
		releaseCallStack.String() + platform.LineBreak +
		"\trelease call stack (2/2 full):" + platform.LineBreak +
		secondCallStack.String()

	messageChan <- nil
}

func monitorCachedObjectReleased(wrappedCachedObject LeakDetectionWrapper, options *LeakDetectionOptions) {
	time.AfterFunc(options.MaxConsumerHoldTime, func() {
		if !wrappedCachedObject.WasReleased() {
			messageChan <- "[objectstorage::leakkdetection] possible leak detected - CachedObject not released for more than " + strconv.Itoa(int(time.Since(wrappedCachedObject.GetRetainTime()).Seconds())) + " seconds:" + platform.LineBreak +
				"\tretained: " + wrappedCachedObject.GetRetainCallStack().ExternalEntryPoint() + platform.LineBreak +
				platform.LineBreak +
				"\tretain call stack (full):" + platform.LineBreak +
				wrappedCachedObject.GetRetainCallStack().String()

			monitorCachedObjectReleased(wrappedCachedObject, options)
		}
	})
}

func registerCachedObjectRetained(wrappedCachedObject LeakDetectionWrapper, options *LeakDetectionOptions) {
	stringKey := string(wrappedCachedObject.Base().key)
	wrappedCachedObject.GetInternalId()

	instanceRegisterMutex.Lock()

	instancesByKey, instancesByKeyExists := instanceRegister[stringKey]
	if !instancesByKeyExists {
		instancesByKey = make(map[int64]LeakDetectionWrapper)

		instanceRegister[stringKey] = instancesByKey
	}
	instancesByKey[wrappedCachedObject.GetInternalId()] = wrappedCachedObject

	if len(instancesByKey) > options.MaxConsumersPerObject {
		var oldestEntry LeakDetectionWrapper = nil
		var oldestTime = time.Now()
		for _, wrappedCachedObject := range instancesByKey {
			if typeutils.IsInterfaceNil(oldestEntry) || wrappedCachedObject.GetRetainTime().Before(oldestTime) {
				oldestEntry = wrappedCachedObject
				oldestTime = wrappedCachedObject.GetRetainTime()
			}
		}

		messageChan <- "[objectstorage::leakkdetection] possible leak detected - more than " + strconv.Itoa(options.MaxConsumersPerObject) + " (" + strconv.Itoa(len(instancesByKey)) + ") CachedObjects in cache:" + platform.LineBreak +
			"\tretained (oldest): " + oldestEntry.GetRetainCallStack().ExternalEntryPoint() + platform.LineBreak +
			"\tretain call stack (oldest full):" + platform.LineBreak +
			oldestEntry.GetRetainCallStack().String() + platform.LineBreak +
			platform.LineBreak +
			"\tretained (current): " + wrappedCachedObject.GetRetainCallStack().ExternalEntryPoint() + platform.LineBreak +
			"\tretain call stack (current full):" + platform.LineBreak +
			wrappedCachedObject.GetRetainCallStack().String()
	}

	instanceRegisterMutex.Unlock()
}

func registerCachedObjectReleased(wrappedCachedObject LeakDetectionWrapper, options *LeakDetectionOptions) {
	stringKey := string(wrappedCachedObject.Base().key)
	wrappedCachedObject.GetInternalId()

	instanceRegisterMutex.Lock()

	instancesByKey, instancesByKeyExists := instanceRegister[stringKey]
	if instancesByKeyExists {
		delete(instancesByKey, wrappedCachedObject.GetInternalId())

		if len(instancesByKey) == 0 {
			delete(instanceRegister, stringKey)
		}
	}

	instanceRegisterMutex.Unlock()
}

// endregion ///////////////////////////////////////////////////////////////////////////////////////////////////////////

// region options //////////////////////////////////////////////////////////////////////////////////////////////////////

type LeakDetectionOptions struct {
	MaxConsumersPerObject int
	MaxConsumerHoldTime   time.Duration
}

// endregion ///////////////////////////////////////////////////////////////////////////////////////////////////////////
