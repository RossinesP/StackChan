/*
 * SPDX-FileCopyrightText: 2026 M5Stack Technology CO LTD
 *
 * SPDX-License-Identifier: MIT
 */
#include "default.h"
#include <assets/assets.h>
#include <algorithm>

using namespace uitk::lvgl_cpp;
using namespace stackchan::avatar;

static constexpr int kAvatarPanelWidth  = 320;
static constexpr int kAvatarPanelHeight = 240;

static uint32_t fit_image_scale(const lv_image_dsc_t& image)
{
    lv_image_header_t header = image.header;

    if ((header.w == 0 || header.h == 0) && lv_image_decoder_get_info(&image, &header) != LV_RESULT_OK) {
        return 256;
    }
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

    _key_elements.leftEye  = std::make_unique<DefaultEyes>(_pannel->get(), primaryColor, secondaryColor, true);
    _key_elements.rightEye = std::make_unique<DefaultEyes>(_pannel->get(), primaryColor, secondaryColor, false);
    _key_elements.mouth    = std::make_unique<DefaultMouth>(_pannel->get(), primaryColor, secondaryColor);
    _key_elements.speechBubble =
        std::make_unique<DefaultSpeechBubble>(_pannel->get(), primaryColor, secondaryColor, font);

    _foreground_image = assets::get_image("avatar_foreground.png");
    if (_foreground_image.data) {
        _foreground = std::make_unique<Image>(_pannel->get());
        setup_avatar_layer(*_foreground, _foreground_image);
        _foreground->moveForeground();
    }
}

Container* DefaultAvatar::getPanel() const
{
    if (_pannel) {
        return _pannel.get();
    }
    return NULL;
}
