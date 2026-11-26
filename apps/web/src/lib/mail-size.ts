// Preflight budget; the server checks the exact serialized MIME length.
export const MAX_TOTAL_ATTACHMENT_BYTES = 25 * 1024 * 1024
export const MAX_ENCODED_MESSAGE_BYTES = 35 * 1024 * 1024

function base64PartBytes(bytes: number) {
  const encoded = 4 * Math.ceil(bytes / 3)
  return encoded + 2 * Math.ceil(encoded / 76)
}

export function estimatedMIMEBytes(
  files: ReadonlyArray<{ size: number }>,
  text: string,
  html: string
) {
  const encoder = new TextEncoder()
  // Reserve MIME boundaries and headers, including encoded attachment names.
  return (
    64 * 1024 +
    files.reduce((total, file) => total + base64PartBytes(file.size) + 1024, 0) +
    base64PartBytes(encoder.encode(text).byteLength) +
    base64PartBytes(encoder.encode(html).byteLength)
  )
}
