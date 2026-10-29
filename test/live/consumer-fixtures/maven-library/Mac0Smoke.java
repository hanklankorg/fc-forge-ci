// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: CC0-1.0

import java.nio.charset.StandardCharsets;
import java.util.Arrays;
import se.digg.cose.AlgorithmID;
import se.digg.cose.Attribute;
import se.digg.cose.COSEObject;
import se.digg.cose.COSEObjectTag;
import se.digg.cose.HeaderKeys;
import se.digg.cose.MAC0COSEObject;

/** Consumer-specific public-API oracle, compiled against downloaded artifacts. */
public final class Mac0Smoke {
  public static void main(String[] args) throws Exception {
    byte[] key = new byte[32];
    Arrays.fill(key, (byte) 0x41);
    byte[] wrong = key.clone();
    wrong[0] ^= 1;
    byte[] payload = "downloaded-library MAC0 oracle".getBytes(StandardCharsets.UTF_8);
    MAC0COSEObject message = new MAC0COSEObject();
    message.addAttribute(HeaderKeys.Algorithm, AlgorithmID.HMAC_SHA_256.AsCBOR(), Attribute.PROTECTED);
    message.SetContent(payload);
    message.Create(key);
    byte[] encoded = message.EncodeToBytes();
    MAC0COSEObject decoded = (MAC0COSEObject) COSEObject.DecodeFromBytes(encoded, COSEObjectTag.MAC0);
    if (!decoded.Validate(key) || !Arrays.equals(payload, decoded.GetContent())) {
      throw new AssertionError("MAC0 round trip failed");
    }
    MAC0COSEObject negative = (MAC0COSEObject) COSEObject.DecodeFromBytes(encoded, COSEObjectTag.MAC0);
    if (negative.Validate(wrong)) {
      throw new AssertionError("MAC0 accepted the wrong key");
    }
    System.out.println("MAC0 round trip and wrong-key rejection passed");
  }
}
