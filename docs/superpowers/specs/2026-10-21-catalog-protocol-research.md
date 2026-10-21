# WhatsApp Business catalog over the multi-device protocol: research brief

Date: 2026-10-21. Status: research only. No code has been written.
Context: litezap (Go, whatsmeow `v0.0.0-20261016130820-0b0a1cdac3e2`). whatsmeow has no catalog API apart from `GetOrderDetails`.

Confidence labels used below:
- **[H] High.** Read directly in current source: the WA Web bundle snapshot, whatsmeow, or Baileys.
- **[M] Medium.** Read in source, but the server's current behaviour is unverified, or the item comes from an older snapshot.
- **[L] Low.** Inferred, or nothing public covers it.

---

## 0. Key findings

1. **The legacy `w:biz:catalog` IQs that Baileys uses appear to be dead for most operations.** This covers `product_catalog`, `collections`, `product_catalog_add`, `product_catalog_edit` and `product_catalog_delete`.
   - Baileys issue [#2717](https://github.com/WhiskeySockets/Baileys/issues/2717) was opened 2026-07-18 and was still reproducing on 2026-09-10 with 7.0.0-rc14. The server **never answers** these IQs: no `<error>`, just a timeout. On the same socket, `w:biz` (business profile) answers instantly. [M]
   - That matches the current WA Web bundle. WA Web no longer builds these stanzas at all. [H]
2. **Current WA Web uses three transports for catalog work:**
   - **(a) Reads (any catalog, including your own public view).** HTTPS GraphQL to `https://graph.whatsapp.com/graphql/catalog`, using a **public app token embedded in the bundle**. No socket and no session. [H]
   - **(b) Writes (product add, edit, delete and visibility; collections; cart setting).** HTTPS GraphQL to `https://graph.facebook.com/graphql`, using a **"WAA" ad-account access token**. The token comes from socket RPCs: `RequestSilentNonce` IQ, then a pushed nonce notification, then the `GetAccessTokenAndSessionCookies` IQ. [H for the code path, L for running it headless]
   - **(c) Socket, still used.** Product image upload (unencrypted media upload, `/product/image`), sending `ProductMessage`, receiving `OrderMessage`, `<notification type="business">` catalog change pushes, and an IQ fallback for `product_list` (get products by id). [H]
3. **Product images use a different upload from normal media.** They do **not** go through the normal encrypted upload. They use an **unencrypted** upload with media type `product-catalog-image` to path **`/product/image/<token>`**, and the resulting **CDN URL** (`https://mmg.whatsapp.net` + `direct_path`) goes into the product. whatsmeow's `Upload` and `RawUpload` cannot produce this path, so we need a small custom HTTP POST using `DangerousInternals().RefreshMediaConn`. [H]
4. **Prices are integers in thousandths of the currency unit** (`priceAmount1000`): R$ 12,50 is sent as `"12500"`. [H]
5. Writing to the catalog from a headless Go client is **feasible but high-friction and high-risk.** Read-only catalog, sending product messages and order handling are low-friction. Plan the feature in two phases (see §10).

---

## 1. Transport map

| Operation | WA Web today | Legacy socket IQ (Baileys) | Recommended for litezap |
|---|---|---|---|
| List catalog (paged) | GraphQL `xwa_product_catalog_get_product_catalog` on graph.whatsapp.com (public token) | `w:biz:catalog` `<product_catalog>`: **times out** | Public GraphQL (§2.1) |
| Get one product | GraphQL `xwa_product_catalog_get_product` | none (there is `product_list`) | Public GraphQL, or IQ `product_list` (§2.2) |
| Get products by id list | GraphQL `xwa_product_catalog_get_product_list`, **IQ fallback still in WA Web** | `<product_list>` smax 21 | Either |
| Create, edit, delete product; set visibility | graph.facebook.com `xfb_whatsapp_catalog_*` (WAA token) | `product_catalog_add/edit/delete`: **times out** | WAA GraphQL flow (§3), phase 2 |
| Collections: list, get one | public GraphQL `xwa_product_catalog_get_collections` / `_get_single_collection` | `<collections>` smax 35: **times out** | Public GraphQL |
| Collections: create, edit, delete, reorder, add or remove items | graph.facebook.com `xfb_whatsapp_catalog_*collection*` | **No public IQ shape known** (smax ids 36/40/43/92 exist) | WAA GraphQL flow, phase 2 |
| Cart enabled | graph.facebook.com `xfb_whatsapp_smb_commerce_settings` | `fb:thrift_iq` `<commerce_settings><cart enabled/>` smax 25 (in WA Web until about Apr 2026) | Try the IQ, else WAA GraphQL |
| Product image upload | unencrypted MMS upload `/product/image` | same | Custom HTTP upload (§4) |
| Send product message | E2E `ProductMessage` | same | `SendMessage` (§5) |
| Order details | GraphQL `xwa_checkout_get_order_info` (public token) | `fb:thrift_iq` `<order op="get">` smax 5 (whatsmeow `GetOrderDetails`) | whatsmeow first, GraphQL fallback (§6) |

---

## 2. Reads

### 2.0 Public catalog GraphQL transport [H]

Source: WA Web `WAWebRelayEnvironment`, `WAWebGraphQLConstants`.

```
POST https://graph.whatsapp.com/graphql/catalog
Content-Type: application/json
Accept: application/json

{
  "access_token": "WA|787118555984857|7bb1544a3599aa180ac9a3f7688ba243",   // WHATSAPP_GRAPHQL_CATALOG_ACCESS_TOKEN (public, in bundle)
  "doc_id": "<persisted query id>",
  "variables": { "request": { ... } },
  "lang": "pt_BR"                       // catalog environment uses "lang" (FB environment uses "locale")
}
```

- **Errors.** Non-2xx responses return `{"error":{...}}`. A 2xx response can still carry `{"errors":[{"code":N,"message":...}]}`. Code `1675002` means unauthorized (`WHATSAPP_GRAPHQL_UNAUTHORIZED_ERROR_CODE`) and `1675004` means `RATE_LIMIT_EXCEEDED`. Catalog-specific codes are listed in §7.
- **doc_ids rotate with WA Web releases** (`WAWebGraphQLPersistedQueries` can override them by operation name). Keep them as config, not constants. Values as of the snapshot:

| Operation | Root field | doc_id |
|---|---|---|
| WAWebQueryCatalogQuery | `xwa_product_catalog_get_product_catalog` | `30445081048424116` |
| WAWebQueryCatalogProductQuery | `xwa_product_catalog_get_product` | `9660926520672123` |
| WAWebQueryProductListCatalogJobQuery | `xwa_product_catalog_get_product_list` | `30125049463760630` |
| WAWebQueryProductCollectionsQuery | `xwa_product_catalog_get_collections` | `9430970660362540` |
| WAWebQueryProductSingleCollectionQuery | `xwa_product_catalog_get_single_collection` | `9546992575408789` |
| WAWebBizQueryOrderJobQuery | `xwa_checkout_get_order_info` | `26593811266898374` |

**JID format.** WA Web sends `<lid>@lid` when the AB prop `wa_catalog_graphql_use_lid_enabled` is on, and `<pn>@s.whatsapp.net` otherwise (`WAWebGetFormattedCatalogJid`). Both are reported to work.

### 2.1 List catalog (pagination) [H request / M response]

```json
{"request":{"product_catalog":{
  "jid":"5511999999999@s.whatsapp.net",
  "allow_shop_source":"ALLOWSHOPSOURCE_TRUE",
  "width":"100","height":"100",
  "limit":"50",
  "after":null,
  "catalog_session_id":null,
  "variant_info_fields":null,"variant_thumbnail_height":null,"variant_thumbnail_width":null
}}}
```

Response: `data.xwa_product_catalog_get_product_catalog.product_catalog`:

```
{ products: [ { id, name, description, price, currency, retailer_id, url, shimmed_url,
                is_hidden ("ISHIDDEN_TRUE"|...), availability ("IN_STOCK"|"OUT_OF_STOCK"|...),
                max_available, sale_price{price,start_date,end_date},
                status_info{status ("APPROVED"|...), can_appeal},
                media{ images[]{id, original_image_url, request_image_url}, videos[]{...} },
                compliance_info{...}, variant_info{...} } ],
  paging: { before, after } }
```

Pass `paging.after` back as `after` for the next page. **Caveat [L]:** the public endpoint probably omits hidden or non-approved items. WA Web reads the owner's full catalog through the authenticated `xfb_whatsapp_catalog` (doc `9957894520961099`) on graph.facebook.com.

Legacy IQ, for reference only (Baileys `getCatalog`; it now times out):

```xml
<iq to="s.whatsapp.net" type="get" xmlns="w:biz:catalog" id="...">
  <product_catalog jid="5511...@s.whatsapp.net" allow_shop_source="true">
    <limit>10</limit><width>100</width><height>100</height><after>CURSOR</after>
  </product_catalog>
</iq>
<!-- reply: <product_catalog><product is_hidden="false"><id/><name/><description/><price/><currency/>
     <retailer_id/><url/><media><image><request_image_url/><original_image_url/></image></media>
     <status_info><status>APPROVED</status><can_appeal/></status_info></product>...
     <paging><after>CURSOR</after></paging></product_catalog> -->
```

### 2.2 Get one product [H]

GraphQL variables:

```json
{"request":{"product":{"jid":"...","product_id":"<id>","width":"100","height":"100",
 "fetch_compliance_info":"false","variant_info_fields":null,
 "variant_thumbnail_height":null,"variant_thumbnail_width":null}}}
```

The response is at `data.xwa_product_catalog_get_product.product_catalog.product`.

**Socket alternative.** `product_list` is still built by the current WA Web as a fallback (`WAWebQueryProductListCatalogJob`). Whether the server still answers it is [M].

```xml
<iq to="s.whatsapp.net" type="get" xmlns="w:biz:catalog" smax_id="21" id="...">
  <product_list jid="5511...@s.whatsapp.net">
    <product><id>123456789</id></product>
    <product><id>987654321</id></product>
    <width>100</width><height>100</height>
    <!-- optional: <direct_connection_encrypted_info>...</direct_connection_encrypted_info> -->
  </product_list>
</iq>
<!-- reply: <iq type="result"><product_list><product>...same product node as above...</product>
     (a product whose <status> equals the INVALID_PRODUCT_TOKEN constant is unavailable)</product_list></iq> -->
```

Product node fields parsed by WA Web (`WAWebBizCatalogParseProduct`):

- Attributes: `is_hidden`, `compliance_category`, `availability`, `is_sanctioned`, `max_available`.
- Children: `id`, `name`, `description`, `price`, `currency`, `retailer_id`, `url`, `shimmed_url`, `belongs_to`, `sale_price{start_date,end_date}`, `status_info{status,can_appeal}`, `media{image{id,request_image_url,original_image_url}, video{original_video_url,thumbnail_url}}`, `compliance_info{country_code_origin, importer_name, importer_address{city,country_code,postal_code,region,...}}`.

### 2.3 Collections: list and get one [H request]

```json
{"request":{"collections":{"biz_jid":"...","collection_limit":"51","item_limit":"51","after":null,
 "width":"100","height":"100","variant_info_fields":null,"variant_thumbnail_height":null,"variant_thumbnail_width":null}}}
```

```json
{"request":{"collection":{"biz_jid":"...","id":"<collectionId>","limit":"20","after":null,
 "width":"100","height":"100","variant_info_fields":null,"variant_thumbnail_height":null,"variant_thumbnail_width":null}}}
```

Responses:
- List: `xwa_product_catalog_get_collections` holds `collections[]` (`id`, `name`, `products[]`, `status_info{status, reject_reason, commerce_url}`) plus paging.
- Single: `xwa_product_catalog_get_single_collection` holds `{collection{id,name,products,status_info}, paging}`.

Legacy IQ (Baileys `getCollections`, smax 35; times out):

```xml
<iq to="s.whatsapp.net" type="get" xmlns="w:biz:catalog" smax_id="35">
  <collections biz_jid="...">
    <collection_limit>51</collection_limit><item_limit>51</item_limit><width>100</width><height>100</height>
  </collections>
</iq>
```

---

## 3. Writes: products, collections, settings

### 3.1 How WA Web writes today [H code, L headless feasibility]

Every write goes to `POST https://graph.facebook.com/graphql` with:

```json
{ "access_token": "<WAA token>", "doc_id": "<id>", "variables": {"input": { ... }}, "locale": "pt_BR" }
```

- On SMB, WA Web also sends the header `X-WA-Device-ID`.
- A multipart form body is used when files are attached.

**Getting the WAA token** (`WAWebFetchAdAccountToken`, `WAWebCTWABizAccessTokenNonceManager`):

1. Send `RequestSilentNonce`:
   ```xml
   <iq xmlns="fb:thrift_iq" smax_id="118" type=? to="s.whatsapp.net"/>
   ```
   The reply is one of:
   - `Success`: `<result/>`
   - `RecoveryRequired`: `<result email="..."/>`, meaning the user must complete an email or ads-recovery step. A headless client cannot finish this.
   - `Error`
2. Wait for the pushed nonce:
   ```xml
   <notification type="business" from="s.whatsapp.net"><wa_ad_account_nonce>NONCE</wa_ad_account_nonce></notification>
   ```
   whatsmeow drops `type="business"` notifications into its default branch (`notification.go`, "Unhandled notification"). **Capturing the nonce needs a whatsmeow patch or fork.**
3. Exchange the nonce for the token:
   ```xml
   <iq xmlns="fb:thrift_iq" smax_id="104" type=?><parameters><code>NONCE</code></parameters></iq>
   ```
   - Success holds `<access_token>`, `<business_person id=...>`, `<session_cookies>` and an optional `<token_type>` (`Strong`/`Weak`).
   - Failures are `IncorrectNonce` (retry once), `TooManyAttempts`, or `Error`.
4. Cache `{bp_id, token}`. Invalidate it on GraphQL code `190` (INVALID_ACCESS_TOKEN). The account is blocked on `2859017` BUSINESS_BANHAMMERED or `2859050` AD_ACCOUNT_LINKING_DISABLED.

The IQ `type` attribute comes from a SMAX mixin that was not inspected [L]. Cobalt (`modules/wire/wire-graphql/.../BpAccessTokenAndSessionCookies*`) models the same flow and is the best Java reference.

### 3.2 Mutations (doc_ids as of the snapshot; they rotate) [H]

| Op | Root field | doc_id | `variables.input` |
|---|---|---|---|
| Create catalog | `xfb_whatsapp_catalog_create` | `29232780583035464` | (not inspected) |
| Add product | `xfb_whatsapp_catalog_add_product` | `24249359867999500` | `{product:{biz_jid, width:"100", height:"100", product_info:P}}` |
| Edit product | `xfb_whatsapp_catalog_edit_product` | `9889773371084956` | `{product:{biz_jid, product_id, width, height, product_info:P}}` |
| Delete products | `xfb_whatsapp_catalog_delete_product` | `9376108569185474` | `{biz_jid, product_ids:[...]}` |
| Hide or show | `xfb_whatsapp_catalog_product_visibility_update` | `9665162096898581` | `{jid, products:[{product_id, is_hidden}]}` |
| Create collection | `xfb_whatsapp_catalog_create_collection` | `29361942130088470` | `{collection:{name, product_ids:[...], biz_jid, catalog_session_id}}` |
| Edit collection, add or remove items | `xfb_whatsapp_catalog_update_collection` | `24486970300891371` | `{collection:{id, biz_jid, catalog_session_id, name?, add?:{ids:[...]}, remove?:{ids:[...]}}}` |
| Delete collections | `xfb_whatsapp_catalog_delete_collections` | `29970196299234260` | `{collections:{collection_ids:[...], biz_jid, catalog_session_id}}` |
| Reorder collections | `xfb_whatsapp_catalog_update_collection_list` | `9930298893688430` | `{biz_jid, move:[{collection_id, from_index, to_index}]}` |
| Commerce settings (cart) | `xfb_whatsapp_smb_commerce_settings` | `9797519763673469` | `{biz_jid, cart_enabled:true}`; response `{cart_enabled}` |
| Owner: fetch catalog, collections, product | `xfb_whatsapp_catalog`, `_collections`, `_product` | `9957894520961099`, `9687699931342731`, `24529100180014015` | |

**`product_info` (P)** is built by `productModelToGraphQLInput`:

```json
{
  "name": "Camiseta",
  "media": { "image": [ {"url": "https://mmg.whatsapp.net/v/t45.../...?..."} ],     // first = head image; CDN URLs from §4
             "video": [ {"url": "..."} ] },                                          // optional
  "is_hidden": false,
  "description": "...",            // omitted when empty
  "url": "https://loja/x",         // omitted when empty
  "retailer_id": "SKU-1",          // omitted when empty; duplicate -> error 3095069
  "currency": "BRL", "price": "12500",   // both only when priceAmount1000 != null; price = amount*1000 as string
  "sale_price": "9990",            // optional
  "compliance_info": { "country_code_origin": "BR", "importer_name": "...", "importer_address": {...} },  // India-only in practice
  "compliance_category": "COUNTRY_ORIGIN_EXEMPT"   // optional
}
```

The response returns the full product, the same shape as §2.1.

### 3.3 Legacy product IQs, for reference only (Baileys; these now time out) [H shape, dead server]

```xml
<iq to="s.whatsapp.net" type="set" xmlns="w:biz:catalog">
  <product_catalog_add v="1">
    <product is_hidden="false" [compliance_category="COUNTRY_ORIGIN_EXEMPT"]>
      <name>Camiseta</name><description>...</description><retailer_id>SKU-1</retailer_id>
      <media><image><url>https://mmg.whatsapp.net/...</url></image></media>
      <price>12500</price><currency>BRL</currency>
      <!-- optional --><compliance_info><country_code_origin>BR</country_code_origin></compliance_info>
    </product>
    <width>100</width><height>100</height>
  </product_catalog_add>
</iq>
<!-- reply: <product_catalog_add><product>...</product></product_catalog_add> -->
```

- **Edit** is `<product_catalog_edit v="1">`, and the same `<product>` starts with `<id>PRODUCT_ID</id>`.
- **Delete** is `<product_catalog_delete v="1"><product><id>X</id></product>...</product_catalog_delete>`. The reply carries the attribute `deleted_count="N"`.
- Baileys' `toProductNode` never serialises `url`. That is a Baileys omission, not a protocol rule.

### 3.4 Cart enabled: socket IQ (in WA Web until about April 2026 as a fallback) [M]

```xml
<iq to="s.whatsapp.net" type="set" xmlns="fb:thrift_iq" smax_id="25" id="...">
  <commerce_settings><cart enabled="true"/></commerce_settings>
</iq>
<!-- reply: <iq type="result"><commerce_settings><cart enabled="true"/></commerce_settings></iq> -->
```

The wa-diff snapshot `02fbd36415` (2026-04-11) used this IQ when GraphQL returned `recovery-required`. Cobalt's `IqUpdateCartEnabledRequest` has the same shape. The current bundle removed it, so the server may or may not still answer it. Test it with a short timeout.

### 3.5 Collections over the socket [L]

The smax ids still exist in `WAWapDeprecatedSmaxID`:

| Operation | smax id |
|---|---|
| CatalogGetSingleCollection | 30 |
| CatalogGetCollections | 35 |
| CatalogCreateCollection | 36 |
| CatalogUpdateCollection | 40 |
| CatalogDeleteCollections | 43 |
| CatalogAppealCollection | 48 |
| UpdateCollectionList | 92 |
| SetCatalogVisibility | 23 |

Problems:
- The **stanza shapes are not in any public source** I found: not wa-diff (it starts in April 2026, already GraphQL-only), not Baileys, Cobalt, AMWAPOS, or the GitHub code search.
- Given that sibling `w:biz:catalog` IQs time out, guessing the shapes is not worthwhile.

**Treat collection writes as GraphQL-only.**

---

## 4. Product image upload [H]

Sources:
- WA Web: `WAWebMediaProduct.uploadProductImage`, `WAWebMmsClientFormatHashUrl`, `WAWebUploadManagerMainThread.unencryptedUpload`.
- Baileys: `uploadingNecessaryImages`, `MEDIA_PATH_MAP`.

- **Media type:** `product-catalog-image`. It is **not** `product` and **not** `biz-product-image`.
- **Upload path:** `/product/image/<token>`.
- **Encryption:** none. Upload the raw JPEG/PNG bytes. There is no media key.
- **Hash and token:** `token = base64url(sha256(plaintext))`, with no padding (`+`→`-`, `/`→`_`, strip `=`).
- **Request:**
  ```
  POST https://<mediaConn.Hosts[0].Hostname>/product/image/<token>?auth=<urlencoded mediaConn.Auth>&token=<token>
  Origin: https://web.whatsapp.com
  Referer: https://web.whatsapp.com/
  Content-Type: application/octet-stream
  <raw bytes>
  ```
- **Response JSON:** `{"url": "...", "direct_path": "/v/t45....?..."}`. Use **`https://mmg.whatsapp.net` + `direct_path`** as the image URL in the product (that is what WA Web does). Baileys also accepts an existing `*.whatsapp.net` URL without re-uploading.
- **Go with whatsmeow:**
  - `cli.Upload` and `DangerousInternals().RawUpload` both resolve the path through the private `mediaTypeToMMSType` map. A custom `MediaType` gives an empty mms type, which is the wrong path.
  - Write a ~40-line uploader instead:
    1. `mc, _ := cli.DangerousInternals().RefreshMediaConn(ctx, false)`.
    2. Build the URL above.
    3. POST it with our own `http.Client`. `mediaHTTP` is not exported, but `SetMediaHTTPClient` exists if we want the same proxy settings.
    4. Retry the other `mc.Hosts`, and call `RefreshMediaConn(ctx, true)` on a 401 or 403.
- Do not reuse the normal encrypted `MediaImage` upload for catalog images. Its blob is encrypted and cannot be fetched as a plain CDN URL.

---

## 5. Sending product and catalog messages [H proto, M render rules]

The proto is in whatsmeow `proto/waE2E/WAWebProtobufsE2E.proto`, message `ProductMessage` (field 30 of `Message`):

```
ProductMessage {
  ProductSnapshot product = 1 {
    ImageMessage productImage = 1;   // a NORMAL encrypted image upload (MediaImage), not the catalog CDN URL
    string productID = 2;            // catalog product id (required for "View" to open the product)
    string title = 3; string description = 4;
    string currencyCode = 5; int64 priceAmount1000 = 6;   // omit both when price is 0
    string retailerID = 7; string URL = 8; uint32 productImageCount = 9;
    string firstImageID = 11; int64 salePriceAmount1000 = 12; string signedURL = 13;
  }
  string businessOwnerJID = 2;       // "<number>@s.whatsapp.net" (WA Web uses legacy/pn form)
  CatalogSnapshot catalog = 4 { ImageMessage catalogImage = 1; string title = 2; string description = 3; }
  string body = 5;                   // caption
  string footer = 6;
  ContextInfo contextInfo = 17;
}
```

**What WA Web sends** (`WAWebProductCatalogMessage.createProductMsgSnapshot`, `WAWebGenerateProductMessageProto`):

- **Fields set:** `businessOwnerJid`, `productId`, `url`, `productImageCount`, `title`, `description`, `currencyCode` with `priceAmount1000` (both null when the price is 0), `salePriceAmount1000` (only when the sale is active), `body` (the caption), `footer`, and `productImage`.
- **`productImage`:** WA Web downloads the product's head image and re-uploads it as a regular encrypted image (normal `cli.Upload(..., whatsmeow.MediaImage)`). It fills `ImageMessage` with url, directPath, mediaKey, the hashes, mimetype, and a JPEG thumbnail.
- **`CatalogSnapshot`:** **not** set by WA Web for single-product sends.

**Minimum for a good render [M]:**
- `product.productImage` with a valid encrypted image and `JPEGThumbnail`, plus `productID`, `title` and `businessOwnerJID`.
- Price and currency are optional.
- Recipients open product details by querying the catalog with `businessOwnerJID` and `productID`. That only works if the product exists, is approved and is not hidden.

**Stanza attributes.** whatsmeow already sets `<enc mediatype="product">` (and `"order"` for `OrderMessage`) in `getMediaTypeFromMessage` (`send.go`). No extra work is needed.

**"Catalog message" (sharing the whole catalog).**
- WA Web shares the catalog as a link: `https://wa.me/c/<number>` in an `ExtendedTextMessage` with a link preview.
- A `ProductMessage` that carries only `catalog` (`CatalogSnapshot`) is a legacy mobile format. Rendering on current clients is unverified [L].
- There is also `ListMessage` with `listType=PRODUCT_LIST` and `ProductListInfo{productSections[{title, products[{productID}]}], headerImage{productID, JPEGThumbnail}, businessOwnerJID}` (multi-product message). Interactive and list messages are unreliable from linked devices and on web/desktop recipients [M].

---

## 6. Orders [H proto, M server]

**Receive.** `waE2E.OrderMessage` (field 38) has these fields:
- Identity and auth: `orderID`, `token` (base64), `sellerJID`.
- Display: `thumbnail`, `itemCount`, `orderTitle`, `message`, `totalAmount1000`, `totalCurrencyCode`.
- State: `status` (INQUIRY=1, ACCEPTED=2, DECLINED=3), `surface` (CATALOG=1).
- Other: `messageVersion`, `orderRequestMessageID`, `catalogType`.

It arrives as a normal `events.Message` with `Message.GetOrderMessage() != nil`.

**Order details, option A** (whatsmeow `GetOrderDetails(ctx, orderID, token)`, in `business.go`):

```xml
<iq to="s.whatsapp.net" type="get" xmlns="fb:thrift_iq" smax_id="5">
  <order op="get" id="ORDER_ID">
    <image_dimensions><width>100</width><height>100</height></image_dimensions>
    <token>BASE64_TOKEN</token>
  </order>
</iq>
<!-- reply: <order id=".." creation_ts=".."><price><subtotal/><total/><currency/><price_status/></price>
     <catalog><id/></catalog><product><id/><name/><price/><currency/><quantity/>
     <image><id/><url/></image><variant_info><properties/></variant_info></product>...</order> -->
```

It returns `types.OrderDetails`. **Note:** WA Web now uses GraphQL instead. Whether this IQ still answers is unverified [M]. Test it before relying on it.

**Order details, option B** (public GraphQL, environment `whatsapp_catalog`, doc `26593811266898374`):

```json
{"request":{"order":{"jid":"<sellerJid>","id":"<orderID>",
  "token":{"sensitive_string_value":"<token>"},
  "image_dimensions":{"height":"100","width":"100"}}}}
```

The response is at `data.xwa_checkout_get_order_info.order`. The image dimensions may be numbers or strings; WA Web passes the caller's values [L].

---

## 7. Error codes

**Socket IQs.**
- Failures come back as `<iq type="error"><error code="N" text="..."/></iq>`; whatsmeow returns `*whatsmeow.IQError{Code, Text}`.
- Codes WA Web maps for catalog work [M]:

| Code | Meaning |
|---|---|
| 400 | Bad request or validation |
| 401 | Not authorised |
| 403 | Not a business, or not allowed |
| 404 | No catalog or product |
| 406 | Field validation (CatalogEditServerError, e.g. duplicate `retailer_id`) |
| 421 | Seen in the mapping, meaning not documented |
| 429 | Rate limited |
| 451 | Commerce disabled by sanctions or region (WA Web throws E451 before even calling when `commerce_sanctioned`) |
| 500, 503 | Server error |

- **The legacy `w:biz:catalog` IQs produce no error at all, only a timeout.** Use a short timeout (about 10 s) so we don't wait out the default IQ timeout.

**GraphQL** (`WAWebMaybeThrowCatalogErrors`, `WAWebGraphQLServerError`):

| Code | WA Web maps to |
|---|---|
| 2498048 | 451, commerce disabled or sanctions |
| 2498049, 2498050, 2498051, 2498052, 2498053, 2498056 | 400; 2498052 on an owner fetch means 404, no catalog |
| 2498054 | 421 |
| 3095069 | 406, duplicate `retailer_id` |
| 2498102, 2498103 | Collection reorder error |
| 190 | INVALID_ACCESS_TOKEN, refresh the WAA token |
| 1675002 | Unauthorized (public token) |
| 1675004 | RATE_LIMIT_EXCEEDED |
| 2859017 | BUSINESS_BANHAMMERED |
| 2859050 | AD_ACCOUNT_LINKING_DISABLED |

---

## 8. Business account, phone sync and review

**Which operations need a Business account:**
- **Business (SMB app) account required** for every catalog write (products, collections, cart) and for having a catalog at all. Creating a catalog uses `xfb_whatsapp_catalog_create`.
- **Any account, even a personal one,** can read a business's public catalog. The public GraphQL endpoint needs no auth at all.
- Any account can receive a `ProductMessage` or `OrderMessage`.
- Sending a `ProductMessage` is technically possible from any account, but it only renders usefully when the referenced product belongs to `businessOwnerJID`.
- Orders are only created against a business catalog with the cart enabled.

**Do edits appear on the phone immediately?**
- The catalog lives server-side in Meta's commerce backend, not in app-state sync. Phones and other companions re-fetch it.
- The server pushes `<notification type="business"><product_catalog><product><id>X</id></product></product_catalog></notification>` for product changes. For collections it carries `<collection id=".."><status_info><status/><reject_reason/><commerce_url/></status_info></collection>`. WA Web acks these and refreshes.
- So edits usually show up within seconds on the other devices. **But new and edited items go through review:** `status_info.status` is `PENDING` until `APPROVED` or `REJECTED`.
- Collections can take **up to 24 h** to be approved before customers see them, per Meta's help content and third-party guides.
- Hidden or rejected items are visible only to the owner.

**What whatsmeow does with these:** whatsmeow does not handle `type="business"` notifications. It only logs them at debug level. It still acks them, so nothing breaks. To react to review-status changes we would need a whatsmeow patch, or to poll.

---

## 9. Ban and risk considerations

1. **Public app token from the WA Web bundle (reads).**
   - This is undocumented use of an endpoint and token meant for the web client. Meta can rotate the token or the doc_ids at any release, which would break reads without warning. Keep both configurable and alert on errors `1675002`/`1675004`.
   - The calls are not tied to the WhatsApp account, so the ban risk to the number is **low**, but **IP-level rate limiting** is likely. Cache results, and don't scrape third-party catalogs in bulk.
2. **WAA ad-account token flow (writes).**
   - It links a "business person" or ads identity to the number. It can hit `RecoveryRequired` (email verification, which is interactive) or `AD_ACCOUNT_LINKING_DISABLED`.
   - Code `2859017` `BUSINESS_BANHAMMERED` exists specifically for this path.
   - Driving it from a server is the most fingerprintable thing on this list: unusual IP, no browser, rapid mutations. Rate-limit writes hard, keep a human-like cadence, and never mass-create products.
   - **Recommendation:** make it opt-in per instance, behind a clear warning.
3. **Commerce Policy.**
   - Every product is reviewed. Repeated rejections, such as prohibited goods or misleading prices, can restrict the catalog or the account (`can_appeal` / appeal mutations exist).
   - Validate inputs before sending: name length, a real image, a valid ISO-4217 currency, a positive integer `price` in thousandths, and a unique `retailer_id`.
4. **Retrying dead IQs.** Retrying dead `w:biz:catalog` IQs in a loop produces unanswered stanzas. That is harmless to the session, but wasteful and abnormal. Probe once per process and remember the result.
5. **Product messages.** These are normal E2E messages, so all of litezap's existing anti-ban rules apply (queue, ramp, window). Product images re-uploaded per send are normal media.

---

## 10. Implementation notes for litezap (Go)

**Plumbing available in whatsmeow (this version):**
- `cli.DangerousInternals().SendIQ(ctx, whatsmeow.DangerousInfoQuery{Namespace, Type, To: types.ServerJID, SMaxID, Content: []waBinary.Node{...}, Timeout})` sends custom IQs. `DangerousInfoQueryType` covers the types.
- `cli.DangerousInternals().RefreshMediaConn(ctx, force)` returns `*MediaConn{Auth, Hosts[].Hostname}`, which the product-image uploader needs.
- `cli.Upload(ctx, jpeg, whatsmeow.MediaImage)` produces `ProductSnapshot.productImage`.
- `cli.GetOrderDetails(ctx, orderID, token)` already exists.
- `cli.SendMessage(ctx, to, &waE2E.Message{ProductMessage: ...})` sends product messages; the `mediatype` attribute is automatic.
- Business profile code already lives in `internal/worker/social.go` and `internal/service/social.go`, which is a natural home.

**Suggested phases:**
- **Phase 1 (low risk):**
  - Catalog read: list, get product, collections. Use the public GraphQL with configurable `access_token` and doc_ids, plus the IQ `product_list` fallback.
  - Send product messages.
  - Order receive and details: `GetOrderDetails` first, falling back to GraphQL.
  - Product image upload helper.
- **Phase 2 (opt-in, high risk):**
  - Writes through the WAA token flow. This needs a whatsmeow patch to surface `<notification type="business"><wa_ad_account_nonce>`, the two SMAX IQs (118, 104), then graph.facebook.com mutations.
  - Cart toggle: try the `fb:thrift_iq` IQ first.

**Before building phase 2, verify on a real Business number** (a one-off probe, 10 s timeout each):
1. Does `product_catalog_add` still time out?
2. Does `product_list` answer?
3. Does `commerce_settings` answer?
4. Does `GetOrderDetails` answer?
5. Does `RequestSilentNonce` return `Success` or `RecoveryRequired`?

---

## 11. Sources

- whatsmeow module cache `go.mau.fi/whatsmeow@v0.0.0-20261016130820-0b0a1cdac3e2`:
  - `business.go` (`GetOrderDetails`) and `types/business.go`
  - `upload.go` (`rawUpload`, mms path), `download.go` (`mediaTypeToMMSType`)
  - `internals.go` (`DangerousInfoQuery`, `SendIQ`, `RefreshMediaConn`, `RawUpload`)
  - `send.go` (`getMediaTypeFromMessage`: product/order), `notification.go` (business type unhandled)
  - `proto/waE2E/WAWebProtobufsE2E.proto` (`ProductMessage`, `OrderMessage`, `ListMessage.ProductListInfo`, `InteractiveMessage.CollectionMessage`/`ShopMessage`)
- Baileys (master):
  - [src/Socket/business.ts](https://github.com/WhiskeySockets/Baileys/blob/master/src/Socket/business.ts)
  - [src/Utils/business.ts](https://github.com/WhiskeySockets/Baileys/blob/master/src/Utils/business.ts)
  - [src/Types/Product.ts](https://github.com/WhiskeySockets/Baileys/blob/master/src/Types/Product.ts)
  - [src/Defaults/index.ts](https://github.com/WhiskeySockets/Baileys/blob/master/src/Defaults/index.ts) (`MEDIA_PATH_MAP` `'product-catalog-image': '/product/image'`)
  - [src/Utils/messages-media.ts](https://github.com/WhiskeySockets/Baileys/blob/master/src/Utils/messages-media.ts) (`getWAUploadToServer`)
- Baileys issue [#2717](https://github.com/WhiskeySockets/Baileys/issues/2717) ("Timeout when trying to get catalog/collections"; 2026-09-10 diagnosis that the IQs are unanswered and WA Web moved to GraphQL).
- WA Web bundle snapshot, [vinikjkkj/wa-diff](https://github.com/vinikjkkj/wa-diff) (`files/`, HEAD at time of research; older snapshot `02fbd36415`, 2026-04-11):
  - Transport and errors: `WAWebRelayEnvironment`, `WAWebGraphQLConstants`, `WAWebGraphQLServerError`, `WAWebMaybeThrowCatalogErrors`
  - Reads: `WAWebQueryCatalog`, `WAWebQueryCatalogProduct`, `WAWebQueryProductCollections`, `WAWebQueryProductSingleCollection`, `WAWebQueryProductListCatalogJob`
  - Writes: `WAWebBizCatalogManagement*` (mutations and doc ids), `WAWebBizCatalogManagementParseProductGraphql` (`productModelToGraphQLInput`), `WAWebProductCollectionsJob`, `WAWebBizCatalog{Add,Edit,DeleteProducts,ProductVisibilitySet}Job`
  - Token flow: `WAWebFetchAdAccountToken`, `WAWebCTWABizAccessTokenNonceManager`, `WASmaxOutBizCtwaAdAccountGetAccessTokenAndSessionCookiesRequest` (smax 104), `WASmaxOutBizAccessTokenRequestSilentNonceRequest` (smax 118), `WASmaxInBizCtwaAdAccountNonceNotificationRequest`
  - Images: `WAWebMediaProduct`, `WAWebMmsClientFormatHashUrl`, `WAWebMmsMediaTypes`
  - Messages and orders: `WAWebProductCatalogMessage`, `WAWebGenerateProductMessageProto`, `WAWebBizQueryOrderJob`
  - Settings, smax ids, notifications: `WAWebBusinessProfileJob` (cart), `WAWapDeprecatedSmaxID`, `WAWebHandleBusinessNotification`
- [Auties00/Cobalt](https://github.com/Auties00/Cobalt): `wire-stanza/.../iq/biz/IqUpdateCartEnabledRequest.java`, `wire-graphql/.../facebook/business/BizCatalogManagement*`, `whatsappWeb/auth/BpAccessTokenAndSessionCookies*`.
- [ZanaNowshad/AMWAPOS](https://github.com/ZanaNowshad/AMWAPOS) `crates/amwapos-hub/src/whatsapp/catalog_proto.rs` (Rust port of the legacy IQs; prices in thousandths).
- Product and collection review timing: third-party guides, e.g. [interakt.shop](https://www.interakt.shop/blog/whatsapp-collections/), [digilogy.co](https://digilogy.co/news/whatsapp-business-catalog-collections/).

---

## 12. Addendum, 2026-10-21: phase 1 built, and clearing profile fields

**Built (phase 1, device validation pending).**
- Reads use the public GraphQL of §2 from the supervisor (`internal/catalog`), through the instance's effective proxy, cached 5 minutes. Token, `lang` and the four doc_ids are config (`whatsapp_catalog`). The product-list IQ fallback (§2.2) was **not** built: one transport is enough until GraphQL fails on a device.
- `type: "product"` sends follow §5 (normal encrypted image with JPEG thumbnail, `businessOwnerJID`, no `CatalogSnapshot`). The catalog-image upload of §4 is not needed for phase 1 and was not built.
- Orders: `OrderMessage` parsed in the worker; the token is stored server-side; details try `GetOrderDetails` (10 s) and fall back to `xwa_checkout_get_order_info`.

**Clearing the business profile's location and hours [L].** Read in the current wa-diff bundle (`WAWebBusinessProfileJob`, `WAWebEditFormBusinessAddressField.react`, `WAWebBusinessHoursModal.react`):
- WA Web **never removes the map pin**: the address field shows "Edit map on mobile", and `editBusinessProfile` sends `<latitude>`/`<longitude>` as `String(value)` only when defined. A null would be sent as the text "null", which WA Web never does.
- WA Web **always sends a timezone** with `<business_hours>` (the browser's when the profile has none) and has no "no hours" action; it may add `<business_hours_note>`.
- WA Web clears the websites with a **contentless** `<website/>` (`wap("website", null)`), not with empty text. The child order is address, latitude, longitude, description, email, website, categories, business_hours.
- So no public client shows the clear encodings. LiteZap tries, behind `location_clear` (request) / `business_location_clear` (config): `empty_elements` (`<latitude/><longitude/>`, like the website clear, default), `empty_content` (`<latitude></latitude>…`) and `empty_address` (`<address/>` alone). Hours clear: `<business_hours/>` with no timezone and no configs. Golden tests pin each encoding (`internal/worker/catalog_test.go`).
