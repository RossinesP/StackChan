/*
 * SPDX-FileCopyrightText: 2026 M5Stack Technology CO LTD
 *
 * SPDX-License-Identifier: MIT
 */
#pragma once
#include "../../avatar/avatar.h"
#include "../../avatar/elements/feature.h"
#include <gif/lvgl_gif.h>
#include <lvgl.h>
#include <smooth_lvgl.hpp>
#include <memory>

namespace stackchan::avatar {

/**
 * @brief
 *
 */
class DefaultAvatar : public Avatar {
public:
    lv_color_t primaryColor   = lv_color_white();
    lv_color_t secondaryColor = lv_color_black();

    ~DefaultAvatar() override;
    void init(lv_obj_t* parent, const lv_font_t* font = &lv_font_montserrat_16);
    uitk::lvgl_cpp::Container* getPanel() const;
    void setEmotion(const Emotion& emotion) override;

private:
    void setFaceAnimation(const Emotion& emotion);
    void setEmotionIcon(const Emotion& emotion);

    std::unique_ptr<uitk::lvgl_cpp::Container> _pannel;
    std::unique_ptr<uitk::lvgl_cpp::Image> _background;
    std::unique_ptr<uitk::lvgl_cpp::Image> _face;
    std::unique_ptr<uitk::lvgl_cpp::Label> _emotion_icon;
    std::unique_ptr<uitk::lvgl_cpp::Image> _foreground;
    std::unique_ptr<LvglGif> _face_gif;
    lv_image_dsc_t _background_image;
    lv_image_dsc_t _face_image;
    lv_image_dsc_t _foreground_image;
    const lv_font_t* _emoji_font = nullptr;
    const char* _face_asset_name = nullptr;
};

/**
 * @brief
 *
 */
class DefaultEyes : public Feature {
public:
    DefaultEyes(lv_obj_t* parent, lv_color_t primaryColor, lv_color_t secondaryColor, bool isLeftEye);
    ~DefaultEyes();

    void setPosition(const uitk::Vector2i& position) override;
    void setWeight(int weight) override;
    void setRotation(int rotation) override;
    void setEmotion(const Emotion& emotion) override;
    void setVisible(bool visible) override;
    void setSize(int size) override;

private:
    bool _is_left_eye    = false;
    int _eyelid_offset_y = 0;

    std::unique_ptr<uitk::lvgl_cpp::Container> _container;
    std::unique_ptr<uitk::lvgl_cpp::Container> _eye;
    std::unique_ptr<uitk::lvgl_cpp::Container> _eyelid;
};

/**
 * @brief
 *
 */
class DefaultMouth : public Feature {
public:
    DefaultMouth(lv_obj_t* parent, lv_color_t primaryColor, lv_color_t secondaryColor);
    ~DefaultMouth();

    void setPosition(const uitk::Vector2i& position) override;
    void setWeight(int weight) override;
    void setRotation(int rotation) override;
    void setVisible(bool visible) override;

private:
    std::unique_ptr<uitk::lvgl_cpp::Container> _mouth;
};

/**
 * @brief
 *
 */
class DefaultSpeechBubble : public SpeechBubble {
public:
    DefaultSpeechBubble(lv_obj_t* parent, lv_color_t primaryColor, lv_color_t secondaryColor, const lv_font_t* font);
    ~DefaultSpeechBubble();

    void setSpeech(std::string_view text) override;
    void clearSpeech() override;
    void setVisible(bool visible) override;
    void setTextFont(void* font) override;

private:
    std::unique_ptr<uitk::lvgl_cpp::Container> _container;
    std::unique_ptr<uitk::lvgl_cpp::Image> _arrow;
    std::unique_ptr<uitk::lvgl_cpp::Container> _bubble;
    std::unique_ptr<uitk::lvgl_cpp::Label> _text;
};

}  // namespace stackchan::avatar
