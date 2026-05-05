package transcript

import "encoding/json"

func jsonUnmarshalString(data []byte, out *string) error { return json.Unmarshal(data, out) }
func jsonUnmarshalArray(data []byte, out *[]Block) error { return json.Unmarshal(data, out) }
