module github.com/getlantern/checkfallbacks

go 1.12

replace github.com/lucas-clemente/quic-go => github.com/getlantern/quic-go v0.7.1-0.20190606183433-1266fdfeb581

replace github.com/marten-seemann/qtls => github.com/marten-seemann/qtls-deprecated v0.0.0-20190207043627-591c71538704

replace github.com/google/netstack => github.com/getlantern/netstack v0.0.0-20190314012628-8999826b821d

replace github.com/refraction-networking/utls => github.com/getlantern/utls v0.0.0-20190606225154-80c3ccb52074

replace github.com/anacrolix/go-libutp => github.com/getlantern/go-libutp v1.0.3

require (
	github.com/getlantern/flashlight v0.0.0-20190607175337-c59641de7f5c
	github.com/getlantern/fronted v0.0.0-20190606212108-e7744195eded
	github.com/getlantern/golog v0.0.0-20170508214112-cca714f7feb5
	github.com/getlantern/keyman v0.0.0-20180207174507-f55e7280e93a
	github.com/getlantern/yaml v0.0.0-20160317154340-79303eb9c0d9
)
