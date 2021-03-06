# RTMP Webhook Authentication

Incoming RTMP streams can be authenticated using Webhooks. To use these webhooks, node operators must implement their own web service / endpoint to be accessed only by the Livepeer node. As new RTMP streams appear, the Livepeer node will call this endpoint to determine whether the given stream is allowed.

To enable webhook authentication functionality, the Livepeer node should be started with the `-authWebhookUrl` flag, along with the webhook endpoint URL.

For example:

```console
livepeer -authWebhookUrl http://ownserver/auth
```

For each incoming RTMP stream, the Livepeer node will make a `POST` request to the `http://ownserver/auth` endpoint, passing the URL of the RTMP request as JSON object.

For example, if the incoming RTMP request was made to `rtmp://livepeer.node/manifest`, the Liverpeer node will provide the following object as a request to the webhook endpoint:

```json
{
    "url": "rtmp://livepeer.node/manifest"
}
```

The webhook server should respond with HTTP status code `200` in order to authenticate / authorize the RTMP stream. A response with a HTTP status code other than `200` will cause the Livepeer node to disconnect the RTMP stream.

The webhook may respond with an empty body.  In this case, the `manifestID` property of the stream will be taken from the RTMP URL.  If the RTMP URL does not specify a manifest id, then it will be generated at random.  Otherwise, the webhook endpoint should respond with a JSON object in the following format:

```json
{
    "manifestID": "ManifestID",
    "streamKey":  "SecretKey",
    "presets":    ["Preset", "Names"],
    "profiles":   [{"name":"ProfileName", "width":320, "height":240, "bitrate":1000000, "fps":30}]
}
```
The Livepeer node will use the returned `manifestID` for the given stream.

The `manifestID` should consist of alphanumeric characters, only.  Please avoid using any punctuation characters or slashes within the `manifestID`

An optional streamKey may be provided in order to protect the RTMP stream from playback. If the streamKey is omitted, a random key will be generated.

Presets can be specified to override the default transcoding options. The available presets are listed [here](https://github.com/livepeer/go-livepeer/blob/master/common/videoprofile_ids.go).

Custom transcoding profiles can be provided if the presets are not sufficient. Given a stream name (manifest ID) of "ManifestID" and a profile name of "ProfileName", the specific profile will be available for playback at `/stream/ManifestID/ProfileName.m3u8`. However, to take advantage of ABR features in HLS players, the top-level stream name should usually be supplied instead, eg `/stream/ManifestID.m3u8` The `bitrate` field is in bits per second. The `fps` field can be omitted to preserve the source frame rate. Both presets and profiles can be used together to specify the desired transcodes.

There is simple webhook authentication server [example](https://github.com/livepeer/go-livepeer/blob/master/cmd/simple_auth_server/simple_auth_server.go).
