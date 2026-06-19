/*
 * SPDX-FileCopyrightText: 2026 M5Stack Technology CO LTD
 *
 * SPDX-License-Identifier: MIT
 */
#include "default.h"

using namespace uitk;
using namespace uitk::lvgl_cpp;
using namespace stackchan::avatar;

static const Vector2i _container_pos  = Vector2i(0, 8);
static const Vector2i _container_size = Vector2i(320, 50);
static const int _text_mx             = 18;
static const int _bubble_width        = 296;
static const int _bubble_height       = 42;

DefaultSpeechBubble::DefaultSpeechBubble(lv_obj_t* parent, lv_color_t primaryColor, lv_color_t secondaryColor,
                                         const lv_font_t* font)
{
    _container = std::make_unique<Container>(parent);
    _container->setRadius(0);
    _container->setAlign(LV_ALIGN_TOP_MID);
    _container->setBorderWidth(0);
    _container->setBgOpa(0);
    _container->removeFlag(LV_OBJ_FLAG_SCROLLABLE);
    _container->setSize(_container_size.x, _container_size.y);
    _container->setPos(_container_pos.x, _container_pos.y);
    _container->setPadding(0, 0, 0, 0);

    _bubble = std::make_unique<Container>(_container->get());
    _bubble->setRadius(LV_RADIUS_CIRCLE);
    _bubble->setAlign(LV_ALIGN_CENTER);
    _bubble->setBorderWidth(0);
    _bubble->setBgColor(primaryColor);
    _bubble->setBgOpa(LV_OPA_90);
    _bubble->removeFlag(LV_OBJ_FLAG_SCROLLABLE);
    _bubble->setSize(_bubble_width, _bubble_height);
    _bubble->setPos(0, 0);

    _text = std::make_unique<Label>(_bubble->get());
    _text->setTextColor(secondaryColor);
    _text->setTextFont(font);
    _text->setTextAlign(LV_TEXT_ALIGN_CENTER);
    _text->setAlign(LV_ALIGN_CENTER);
    _text->setPos(0, 0);
    _text->setWidth(_bubble_width - _text_mx * 2);
    _text->setLongMode(LV_LABEL_LONG_MODE_SCROLL_CIRCULAR);

    clearSpeech();
}

DefaultSpeechBubble::~DefaultSpeechBubble()
{
    _text.reset();
    _bubble.reset();
    _arrow.reset();
    _container.reset();
}

void DefaultSpeechBubble::setSpeech(std::string_view text)
{
    if (text.empty()) {
        clearSpeech();
        return;
    }

    _text->setText(text);
    setVisible(true);
}

void DefaultSpeechBubble::clearSpeech()
{
    _text->setText("");
    setVisible(false);
}

void DefaultSpeechBubble::setVisible(bool visible)
{
    SpeechBubble::setVisible(visible);

    _container->setHidden(!visible);
    if (visible) {
        _container->moveForeground();
    }
}

void DefaultSpeechBubble::setTextFont(void* font)
{
    if (_text && font) {
        _text->setTextFont((lv_font_t*)font);
    }
}
