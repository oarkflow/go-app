package qrcode

type DataTooLongException struct {
	Msg string
}

func (d *DataTooLongException) Error() string {
	return d.Msg
}
