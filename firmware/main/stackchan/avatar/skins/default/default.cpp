/*
 * SPDX-FileCopyrightText: 2026 M5Stack Technology CO LTD
 *
 * SPDX-License-Identifier: MIT
 */
#include "default.h"
#include <assets/assets.h>
#include <font_emoji.h>
#include <gif/lvgl_gif.h>
#include <algorithm>

using namespace uitk::lvgl_cpp;
using namespace stackchan::avatar;

static constexpr int kAvatarPanelWidth  = 320;
static constexpr int kAvatarPanelHeight = 240;
static constexpr int kFaceOffsetY       = 18;
static constexpr int kEmotionIconSize   = 32;
static constexpr int kEmotionIconGap    = 4;
static constexpr uint32_t kFaceMinFrameDelayMs = 100;
static constexpr const char* kNeutralFaceAsset = "avatar_face_neutral.gif";

static const char* face_asset_for_emotion(const Emotion& emotion)
{
    switch (emotion) {
        case Emotion::Neutral:
        case Emotion::Happy:
        case Emotion::Angry:
        case Emotion::Sad:
        case Emotion::Doubt:
        case Emotion::Sleepy:
        default:
            return kNeutralFaceAsset;
    }
}

static const char* emoji_for_emotion(const Emotion& emotion)
{
    switch (emotion) {
        case Emotion::Happy:
            return "\xF0\x9F\x99\x82";  // slightly smiling face
        case Emotion::Angry:
            return "\xF0\x9F\x98\xA0";  // angry face
        case Emotion::Sad:
            return "\xF0\x9F\x98\x94";  // pensive face
        case Emotion::Doubt:
            return "\xF0\x9F\xA4\x94";  // thinking face
        case Emotion::Sleepy:
            return "\xF0\x9F\x98\xB4";  // sleeping face
        case Emotion::Neutral:
        default:
            return "\xF0\x9F\x98\xB6";  // face without mouth
    }
}

static lv_image_header_t image_header_for(const lv_image_dsc_t& image)
{
    lv_image_header_t header = image.header;
    if ((header.w == 0 || header.h == 0) && lv_image_decoder_get_info(&image, &header) != LV_RESULT_OK) {
        return image.header;
    }
    return header;
}

static uint32_t fit_image_scale(const lv_image_dsc_t& image)
{
    lv_image_header_t header = image_header_for(image);
    if (header.w == 0 || header.h == 0) {
        return 256;
    }

    uint32_t scale_x = kAvatarPanelWidth * 256 / header.w;
    uint32_t scale_y = kAvatarPanelHeight * 256 / header.h;
    return std::max<uint32_t>(1, std::min(scale_x, scale_y));
}

static void setup_avatar_layer(Image& image, const lv_image_dsc_t& image_dsc)
{
    image.setSrc(&image_dsc);
    image.setAlign(LV_ALIGN_CENTER);
    image.setPos(0, 0);
    image.setScale(fit_image_scale(image_dsc));
}

DefaultAvatar::~DefaultAvatar() = default;

void DefaultAvatar::init(lv_obj_t* parent, const lv_font_t* font)
{
    _pannel = std::make_unique<Container>(parent);
    _pannel->align(LV_ALIGN_CENTER, 0, 0);
    _pannel->setSize(kAvatarPanelWidth, kAvatarPanelHeight);
    _pannel->setRadius(0);
    _pannel->setBorderWidth(0);
    _pannel->setBgColor(secondaryColor);
    _pannel->removeFlag(LV_OBJ_FLAG_SCROLLABLE);

    _background_image = assets::get_image("avatar_background.png");
    if (_background_image.data) {
        _background = std::make_unique<Image>(_pannel->get());
        setup_avatar_layer(*_background, _background_image);
        _background->moveBackground();
    }

    _face = std::make_unique<Image>(_pannel->get());
    _face->setAlign(LV_ALIGN_CENTER);
    _face->setPos(0, 0);

    _emoji_font = font_emoji_32_init();
    _emotion_icon = std::make_unique<Label>(_pannel->get());
    _emotion_icon->setAlign(LV_ALIGN_CENTER);
    _emotion_icon->setSize(kEmotionIconSize, kEmotionIconSize);
    _emotion_icon->setWidth(kEmotionIconSize);
    _emotion_icon->setTextAlign(LV_TEXT_ALIGN_CENTER);
    _emotion_icon->setLongMode(LV_LABEL_LONG_MODE_CLIP);
    if (_emoji_font) {
        _emotion_icon->setTextFont(_emoji_font);
    }
    setEmotionIcon(_emotion);

    _key_elements.leftEye = std::make_unique<Feature>();
    _key_elements.leftEye->setVisible(false);
    _key_elements.rightEye = std::make_unique<Feature>();
    _key_elements.rightEye->setVisible(false);
    _key_elements.mouth = std::make_unique<Feature>();
    _key_elements.mouth->setVisible(false);
    _key_elements.speechBubble =
        std::make_unique<DefaultSpeechBubble>(_pannel->get(), primaryColor, secondaryColor, font);

    setFaceAnimation(_emotion);

    _foreground_image = assets::get_image("avatar_foreground.png");
    if (_foreground_image.data) {
        _foreground = std::make_unique<Image>(_pannel->get());
        setup_avatar_layer(*_foreground, _foreground_image);
        _foreground->moveForeground();
    }
}

void DefaultAvatar::setEmotion(const Emotion& emotion)
{
    Avatar::setEmotion(emotion);
    setEmotionIcon(emotion);
    setFaceAnimation(emotion);
}

void DefaultAvatar::setEmotionIcon(const Emotion& emotion)
{
    if (!_emotion_icon) {
        return;
    }
    _emotion_icon->setText(emoji_for_emotion(emotion));
}

void DefaultAvatar::setFaceAnimation(const Emotion& emotion)
{
    const char* asset_name = face_asset_for_emotion(emotion);
    if (_face_asset_name == asset_name && _face_gif && _face_gif->IsLoaded()) {
        return;
    }

    _face_asset_name = asset_name;
    _face_gif.reset();
    _face_image = assets::get_image(asset_name);
    if (!_face || !_face_image.data) {
        return;
    }

    _face_gif = std::make_unique<LvglGif>(&_face_image);
    if (!_face_gif->IsLoaded()) {
        _face_gif.reset();
        return;
    }

    setup_avatar_layer(*_face, *_face_gif->image_dsc());
    _face->setPos(0, kFaceOffsetY);
    if (_emotion_icon) {
        lv_image_header_t header = image_header_for(*_face_gif->image_dsc());
        uint32_t scale = fit_image_scale(*_face_gif->image_dsc());
        int display_w = header.w * scale / 256;
        int display_h = header.h * scale / 256;
        int icon_x = display_w / 2 + kEmotionIconSize / 2 + kEmotionIconGap;
        int icon_y = kFaceOffsetY - display_h / 2 + kEmotionIconSize / 2;
        _emotion_icon->setPos(icon_x, icon_y);
    }
    _face_gif->SetFrameCallback([this]() {
        if (_face && _face_gif) {
            _face->setSrc(_face_gif->image_dsc());
        }
    });
    _face_gif->SetMinFrameDelay(kFaceMinFrameDelayMs);
    _face_gif->Start();
}

Container* DefaultAvatar::getPanel() const
{
    if (_pannel) {
        return _pannel.get();
    }
    return NULL;
}
